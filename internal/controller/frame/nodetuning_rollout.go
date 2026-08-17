/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/agent"
)

// This file is the disruptive half of the NodeTuning lifecycle: once a node's
// change is approved for the live generation (the approval gate in
// nodetuning_controller.go), it cordons the node, evicts its pods through the
// eviction API so PodDisruptionBudgets are honoured, asks the node agent to
// schedule a detached restart, waits for the unit to come back, and uncordons.
//
// Four rules here are not style, they are scars:
//
//  1. A restart is verified by the unit's ActiveEnterTimestamp moving, never
//     by the node flapping NotReady. k3s-agent comes back in seconds while a
//     node only reports NotReady after roughly forty seconds of missed lease,
//     so a healthy restart usually never flaps the node at all: a wait keyed
//     on readiness reports failure on success. Readiness is still required
//     before uncordoning — it is the precondition for handing workloads back,
//     not the evidence that anything restarted.
//
//  2. Every probe in the wait tolerates failure. Restarting k3s on the server
//     node takes the apiserver down for seconds, so the controller's own
//     reads are *expected* to fail mid-wait. A loop that treats the first
//     error as fatal abandons the node mid-operation — cordoned, drained, and
//     silently — which is indistinguishable from a restart that never fired.
//
//  3. One node at a time, and never while any other node is not Ready.
//     Losing one node is a rolling operation; losing two on a three-node
//     cluster is an outage.
//
//  4. A failure halts the whole campaign. The node stays cordoned, the reason
//     lands in its status entry, and no other node is touched. A partially
//     tuned cluster is recoverable; a rollout that keeps going through
//     failures is not.
//
// Rule 3 in particular is enforced deliberately rather than by accident. An
// earlier revision leaned on Go slices of corev1.Node sharing their
// Annotations maps, so cordoning one node "happened to" be visible to the
// next node in the same pass — which also meant a *failed* patch released the
// lock just as effectively as a successful one. The lock is now an explicit
// value (campaignLock), seeded from the API server and moved only by writes
// that actually succeeded.
const (
	// defaultDrainTimeout bounds the eviction phase. Generous on purpose: a
	// PodDisruptionBudget refusing an eviction is the budget doing its job,
	// and giving up early would either strand the node or tempt a caller into
	// deleting pods the budget was protecting.
	defaultDrainTimeout = 10 * time.Minute

	// defaultRestartTimeout bounds the wait for the unit to come back after
	// the restart was requested. k3s takes seconds; minutes here is the
	// difference between "slow" and "never came back", and "never came back"
	// is the failure that must halt the campaign.
	defaultRestartTimeout = 5 * time.Minute

	// rolloutRequeueInterval re-drives a node that is mid-rollout. Nothing
	// else would: the agent's answer arrives as an annotation on the Node,
	// which the controller watches, but a drain that is merely waiting for
	// pods to go produces no watch event at all.
	rolloutRequeueInterval = 10 * time.Second
)

// releaseHaltHint is appended to every failure message. The procedure it
// names is the one that actually works — see TuningRolloutStartedAnnotation's
// doc comment, and startRollout, which clears the stale request and failure
// markers so a released node is retried instead of instantly re-failed.
func releaseHaltHint(nodeName string) string {
	return fmt.Sprintf("release with `kubectl annotate node %s %s-` to retry", nodeName, framev1beta1.TuningRolloutStartedAnnotation)
}

func (r *NodeTuningReconciler) drainTimeout() time.Duration {
	if r.DrainTimeout > 0 {
		return r.DrainTimeout
	}
	return defaultDrainTimeout
}

func (r *NodeTuningReconciler) restartTimeout() time.Duration {
	if r.RestartTimeout > 0 {
		return r.RestartTimeout
	}
	return defaultRestartTimeout
}

// reader returns the client this controller reads nodes and pods with. See
// APIReader's doc: both reads need to see the API server rather than a cache.
func (r *NodeTuningReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// campaignLock is the "one node at a time" lock as one reconcile pass knows
// it. It is seeded from a read of the API server (never the manager's cache:
// a reconcile queued before another one cordoned a node would otherwise see a
// pre-cordon snapshot, find no lock, and take a second node down), and it
// moves only when a write actually succeeded.
type campaignLock struct {
	holder string
}

// newCampaignLock finds the node already holding the lock, if any. Nodes come
// back from the API server sorted by name, so a cluster that somehow has two
// held nodes picks the same one every pass rather than oscillating.
func newCampaignLock(nodes []corev1.Node) *campaignLock {
	for i := range nodes {
		if rolloutInFlight(&nodes[i]) {
			return &campaignLock{holder: nodes[i].Name}
		}
	}
	return &campaignLock{}
}

// heldByOther returns the name of the node holding the lock when it is not
// name, or "" when name may proceed.
func (l *campaignLock) heldByOther(name string) string {
	if l.holder != "" && l.holder != name {
		return l.holder
	}
	return ""
}

func (l *campaignLock) acquire(name string) { l.holder = name }

func (l *campaignLock) release(name string) {
	if l.holder == name {
		l.holder = ""
	}
}

// rolloutInFlight reports whether this node is mid-rollout (or stuck in a
// failed one). It is deliberately the presence of an annotation the
// controller itself wrote, not a phase in some NodeTuning's status: it is the
// one piece of state that is per-node, survives the controller restarting,
// and is visible to every NodeTuning at once — which is exactly what "one
// node at a time, cluster-wide" and "a failure halts the campaign" both need.
func rolloutInFlight(node *corev1.Node) bool {
	_, ok := node.Annotations[framev1beta1.TuningRolloutStartedAnnotation]
	return ok
}

// rolloutOwnedBy reports whether node is mid-rollout under ntName. A node is
// driven only by the NodeTuning that cordoned it, so two objects can never
// both think they are mid-restart on one node.
func rolloutOwnedBy(node *corev1.Node, ntName string) bool {
	return rolloutInFlight(node) && node.Annotations[framev1beta1.TuningRolloutOwnerAnnotation] == ntName
}

// rolloutHalted reports whether this node's rollout gave up and is waiting
// for a human. Keyed on the controller's own failure marker rather than on a
// Failed phase in status: other things write that phase too (an overlapping
// selector, for one), and a node frozen forever over a configuration error
// that has since been fixed is a leak, not a halt.
func rolloutHalted(node *corev1.Node) bool {
	_, ok := node.Annotations[framev1beta1.TuningRolloutFailedAnnotation]
	return ok
}

// nodeIsReady reports whether node's Ready condition is True. A node with no
// conditions at all — one that has never reported — is not Ready.
func nodeIsReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// notReadyNodes returns the sorted names of every node that is not Ready,
// including the rollout's own target: a node that is not reporting is not a
// node to start taking apart either.
func notReadyNodes(nodes []corev1.Node) []string {
	var names []string
	for i := range nodes {
		if !nodeIsReady(&nodes[i]) {
			names = append(names, nodes[i].Name)
		}
	}
	sort.Strings(names)
	return names
}

// annotationTime parses one of the protocol's RFC3339Nano annotations. Every
// caller in the wait loop treats an error as "not yet", never as fatal.
func annotationTime(node *corev1.Node, key string) (time.Time, error) {
	raw, ok := node.Annotations[key]
	if !ok {
		return time.Time{}, fmt.Errorf("node %s has no %s annotation", node.Name, key)
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("node %s: %s=%q is not an RFC3339 timestamp", node.Name, key, raw)
	}
	return t, nil
}

// restartedForGeneration reports whether this node already had its restart
// for this exact generation. One restart per approval: immediately after a
// verified restart the agent has not re-observed yet, so spec still disagrees
// with observed — and re-entering the rollout on that disagreement would
// restart the node again, and again, forever, on a single approval.
//
// RestartedGeneration, not AppliedGeneration: the latter is also advanced by
// a node merely being InSync, so using it would permanently refuse to restart
// a node that reached this generation without one.
func restartedForGeneration(ns framev1beta1.NodeTuningNodeStatus, generation int64) bool {
	return ns.RestartedAt != nil && ns.RestartedGeneration == generation
}

// patchNode applies mutate to a copy of node and patches the difference,
// keeping every node write a merge patch rather than a full update that could
// clobber a concurrent writer (the agent writes annotations on this same
// object every tick). On failure the caller's node object is left mutated but
// the server is not — so no caller may treat a mutation as committed until
// this returns nil.
func (r *NodeTuningReconciler) patchNode(ctx context.Context, node *corev1.Node, mutate func(*corev1.Node)) error {
	patch := client.MergeFrom(node.DeepCopy())
	mutate(node)
	return r.Patch(ctx, node, patch)
}

func setNodeAnnotation(node *corev1.Node, key, value string) {
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Annotations[key] = value
}

// clearRolloutAnnotations removes every annotation this controller owns,
// leaving the agent's two alone.
func clearRolloutAnnotations(node *corev1.Node) {
	for _, key := range []string{
		framev1beta1.TuningRolloutStartedAnnotation,
		framev1beta1.TuningRolloutOwnerAnnotation,
		framev1beta1.TuningRolloutFailedAnnotation,
		framev1beta1.TuningRestartBaselineAnnotation,
		framev1beta1.TuningRestartRequestedAnnotation,
	} {
		delete(node.Annotations, key)
	}
}

// releaseNode uncordons node and drops every rollout annotation, giving up
// the cluster-wide lock. Used when the owning NodeTuning is deleted: leaving
// a node cordoned with no object left to drive it strands both the node and
// every other node's rollout behind a lock nothing will ever release.
func (r *NodeTuningReconciler) releaseNode(ctx context.Context, node *corev1.Node) error {
	return r.patchNode(ctx, node, func(n *corev1.Node) {
		n.Spec.Unschedulable = false
		clearRolloutAnnotations(n)
	})
}

// startRollout is everything that has to be true before a node is touched at
// all. Every refusal here leaves the node exactly as it was — uncordoned,
// undrained, unannotated — and says why in the status message, because a
// refusal nobody can read is the same as a hang.
func (r *NodeTuningReconciler) startRollout(
	ctx context.Context,
	nt *framev1beta1.NodeTuning,
	node *corev1.Node,
	ns framev1beta1.NodeTuningNodeStatus,
	reason string,
	allNodes []corev1.Node,
	lock *campaignLock,
) framev1beta1.NodeTuningNodeStatus {
	log := logf.FromContext(ctx)

	if other := lock.heldByOther(node.Name); other != "" {
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf(
			"waiting: node %s is mid-rollout or left cordoned by a failed one (one node at a time; a failed node halts the campaign until %s is cleared from it)",
			other, framev1beta1.TuningRolloutStartedAnnotation)
		return ns
	}

	if notReady := notReadyNodes(allNodes); len(notReady) > 0 {
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf(
			"refusing to start: node(s) %s are not Ready; taking a second node down is an outage, not a rolling restart",
			strings.Join(notReady, ", "))
		return ns
	}

	unit := node.Annotations[framev1beta1.TuningUnitAnnotation]
	if unit == "" {
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf("waiting for the node agent to report %s", framev1beta1.TuningUnitAnnotation)
		return ns
	}
	// The unit name arrives on an annotation, which is not a trustworthy
	// channel: the agent runs privileged with hostPID, so "restart this unit"
	// is host root. The compile-time allowlist is the boundary (see
	// internal/agent's restartableUnits), and an unlisted unit is a refusal a
	// human must look at, not something to retry quietly.
	if !agent.IsRestartable(unit) {
		ns.Phase = framev1beta1.PhaseFailed
		ns.Message = fmt.Sprintf("refusing to restart unit %q on %s: not on the compile-time restart allowlist", unit, node.Name)
		return ns
	}

	// Not a baseline, just proof one will be obtainable: without the unit's
	// ActiveEnterTimestamp there is nothing a later reading could be compared
	// against, so "did it come back" would be unanswerable, and cordoning and
	// draining for a restart that can never be verified is strictly worse
	// than waiting for the agent to report. The baseline itself is captured
	// at request time, not here — see drainAndRequest.
	if _, err := annotationTime(node, framev1beta1.TuningUnitActiveEnterAnnotation); err != nil {
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf("waiting for a verifiable baseline: %v", err)
		return ns
	}

	// Before the first disruptive write, not after: the finalizer is what
	// guarantees a deletion of this object still gets to release whatever it
	// cordons below.
	if err := r.ensureFinalizer(ctx, nt); err != nil {
		log.Error(err, "adding the rollout finalizer", "nodeTuning", nt.Name)
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf("preparing to cordon %s: %v", node.Name, err)
		return ns
	}

	now := time.Now()
	if err := r.patchNode(ctx, node, func(n *corev1.Node) {
		n.Spec.Unschedulable = true
		setNodeAnnotation(n, framev1beta1.TuningRolloutStartedAnnotation, now.Format(time.RFC3339Nano))
		setNodeAnnotation(n, framev1beta1.TuningRolloutOwnerAnnotation, nt.Name)
		// A rollout always starts from a clean slate. A previous attempt's
		// request would otherwise be read as this attempt's — its timestamp
		// already past the deadline, so the node fails instantly and retakes
		// the lock forever, and the agent, which acts on request values it
		// has not seen before, would never act on it either. This is what
		// makes clearing the started annotation a working recovery rather
		// than a documented one.
		delete(n.Annotations, framev1beta1.TuningRestartRequestedAnnotation)
		delete(n.Annotations, framev1beta1.TuningRestartBaselineAnnotation)
		delete(n.Annotations, framev1beta1.TuningRolloutFailedAnnotation)
	}); err != nil {
		// Tolerated and retried, not failed: nothing has happened yet. The
		// lock is untouched, because nothing was committed.
		log.Error(err, "cordoning node for tuning restart", "node", node.Name)
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf("cordoning %s: %v", node.Name, err)
		return ns
	}
	lock.acquire(node.Name)

	r.Recorder.Eventf(nt, corev1.EventTypeNormal, "NodeCordoned",
		"Cordoned %s to restart %s.service: %s", node.Name, unit, reason)
	ns.Phase = framev1beta1.PhaseApplying
	ns.Message = fmt.Sprintf("cordoned; draining before restarting %s.service (%s)", unit, reason)
	return ns
}

// continueRollout drives a node that is already cordoned: drain, then ask,
// then wait. It is called on every reconcile of a node this object cordoned,
// whatever the diff currently says and whether or not the node still matches
// the selector — a node this controller cordoned must be uncordoned by this
// controller.
func (r *NodeTuningReconciler) continueRollout(
	ctx context.Context,
	nt *framev1beta1.NodeTuning,
	node *corev1.Node,
	ns framev1beta1.NodeTuningNodeStatus,
	lock *campaignLock,
) framev1beta1.NodeTuningNodeStatus {
	log := logf.FromContext(ctx)
	unit := node.Annotations[framev1beta1.TuningUnitAnnotation]

	requested, requestErr := annotationTime(node, framev1beta1.TuningRestartRequestedAnnotation)
	if requestErr != nil {
		return r.drainAndRequest(ctx, nt, node, ns, unit)
	}

	baseline, baselineErr := annotationTime(node, framev1beta1.TuningRestartBaselineAnnotation)
	current, currentErr := annotationTime(node, framev1beta1.TuningUnitActiveEnterAnnotation)

	// The one piece of evidence that counts. Note what is NOT here: no check
	// on the node having gone NotReady. It usually never does.
	if baselineErr == nil && currentErr == nil && current.After(baseline) {
		if !nodeIsReady(node) {
			if time.Since(requested) > r.restartTimeout() {
				return r.failRollout(ctx, nt, node, ns,
					"%s.service restarted at %s but %s never reported Ready within %s; leaving it cordoned (%s)",
					unit, current.Format(time.RFC3339), node.Name, r.restartTimeout(), releaseHaltHint(node.Name))
			}
			ns.Phase = framev1beta1.PhaseApplying
			ns.Message = fmt.Sprintf("%s.service restarted at %s; waiting for %s to report Ready before uncordoning",
				unit, current.Format(time.RFC3339), node.Name)
			return ns
		}

		if err := r.patchNode(ctx, node, func(n *corev1.Node) {
			n.Spec.Unschedulable = false
			clearRolloutAnnotations(n)
		}); err != nil {
			// Tolerated and retried: the restart is verified either way, and
			// re-running this branch is harmless. The lock is NOT released —
			// the node is still cordoned on the server, and letting the next
			// node start while this one is unfinished is exactly the
			// two-nodes-down outage rule 3 exists to prevent.
			log.Error(err, "uncordoning node after tuning restart", "node", node.Name)
			ns.Phase = framev1beta1.PhaseApplying
			ns.Message = fmt.Sprintf("uncordoning %s: %v", node.Name, err)
			return ns
		}
		lock.release(node.Name)

		r.Recorder.Eventf(nt, corev1.EventTypeNormal, "RestartVerified",
			"%s.service on %s restarted at %s; node uncordoned", unit, node.Name, current.Format(time.RFC3339))
		// RestartedAt is the unit's own ActiveEnterTimestamp rather than "when
		// the controller noticed": Task 7 compares pod start times against it,
		// and a pod started between the real restart and this reconcile did
		// inherit the new setting.
		restartedAt := metav1.NewTime(current)
		ns.RestartedAt = &restartedAt
		ns.RestartedGeneration = nt.Generation
		ns.AppliedGeneration = nt.Generation
		ns.Phase = framev1beta1.PhaseApplying
		ns.Message = fmt.Sprintf("%s.service restarted at %s; uncordoned, waiting for the agent to re-observe",
			unit, current.Format(time.RFC3339))
		return ns
	}

	if time.Since(requested) > r.restartTimeout() {
		detail := "its ActiveEnterTimestamp has not advanced"
		if currentErr != nil {
			detail = currentErr.Error()
		}
		return r.failRollout(ctx, nt, node, ns,
			"%s.service on %s did not come back within %s of the restart request: %s; leaving it cordoned and halting the rollout (%s)",
			unit, node.Name, r.restartTimeout(), detail, releaseHaltHint(node.Name))
	}

	// Everything below is a probe that failed or has not moved yet, and none
	// of it is fatal: mid-restart the apiserver may be down, the agent may not
	// have re-published, and the annotation may be missing or garbage. Only
	// the deadline above ever turns this into a failure.
	ns.Phase = framev1beta1.PhaseApplying
	switch {
	case currentErr != nil:
		ns.Message = fmt.Sprintf("waiting for %s.service to restart (no readable timestamp yet: %v)", unit, currentErr)
	case baselineErr != nil:
		ns.Message = fmt.Sprintf("waiting for %s.service to restart (no readable baseline yet: %v)", unit, baselineErr)
	default:
		ns.Message = fmt.Sprintf("waiting for %s.service to restart (still at %s)", unit, current.Format(time.RFC3339))
	}
	return ns
}

// drainAndRequest evicts what is left on the node and, once nothing blocks,
// asks the agent for the restart by annotating the node.
func (r *NodeTuningReconciler) drainAndRequest(
	ctx context.Context,
	nt *framev1beta1.NodeTuning,
	node *corev1.Node,
	ns framev1beta1.NodeTuningNodeStatus,
	unit string,
) framev1beta1.NodeTuningNodeStatus {
	log := logf.FromContext(ctx)

	remaining, err := r.evictPods(ctx, node.Name)
	if err != nil {
		// Listing pods can fail for the same reason everything else can here.
		log.Error(err, "draining node", "node", node.Name)
		ns.Phase = framev1beta1.PhaseApplying
		ns.Message = fmt.Sprintf("draining %s: %v", node.Name, err)
		return ns
	}

	if remaining > 0 {
		if started, sErr := annotationTime(node, framev1beta1.TuningRolloutStartedAnnotation); sErr == nil &&
			time.Since(started) > r.drainTimeout() {
			return r.failRollout(ctx, nt, node, ns,
				"draining %s did not finish within %s (%d pod(s) still there); leaving it cordoned and halting the rollout (%s)",
				node.Name, r.drainTimeout(), remaining, releaseHaltHint(node.Name))
		}
		ns.Phase = framev1beta1.PhaseApplying
		ns.Message = fmt.Sprintf("draining: %d pod(s) still on %s", remaining, node.Name)
		return ns
	}

	// The baseline is read here, at the moment of asking, and not back at
	// cordon time: a drain can take minutes, and any restart of the unit
	// during it (an admin, a crash loop, a package upgrade) would sit between
	// an older baseline and the first post-request probe, which would then
	// "verify" a restart that this controller never asked for. The node would
	// be uncordoned and given workloads back at the exact moment the agent
	// finally acts on the request.
	baseline, err := annotationTime(node, framev1beta1.TuningUnitActiveEnterAnnotation)
	if err != nil {
		// Not a failure: without a baseline the request cannot be verified,
		// so it simply is not made yet. The drain deadline still applies.
		ns.Phase = framev1beta1.PhaseApplying
		ns.Message = fmt.Sprintf("drained; waiting for a verifiable baseline before requesting the restart: %v", err)
		return ns
	}

	// The controller never restarts anything itself and never runs a command
	// on a node: bouncing k3s from a pod would kill the kubelet that owns the
	// caller. It asks, and the agent schedules a detached transient unit and
	// exits (see the design doc's "Restarting a node from a pod kills the
	// caller"). The annotation carries no unit name on purpose — the agent
	// detects its own and checks the same allowlist, so nothing that can write
	// a Node annotation can choose what gets restarted.
	if err := r.patchNode(ctx, node, func(n *corev1.Node) {
		setNodeAnnotation(n, framev1beta1.TuningRestartBaselineAnnotation, baseline.Format(time.RFC3339Nano))
		setNodeAnnotation(n, framev1beta1.TuningRestartRequestedAnnotation, time.Now().Format(time.RFC3339Nano))
	}); err != nil {
		log.Error(err, "requesting restart", "node", node.Name)
		ns.Phase = framev1beta1.PhaseApplying
		ns.Message = fmt.Sprintf("requesting restart on %s: %v", node.Name, err)
		return ns
	}

	r.Recorder.Eventf(nt, corev1.EventTypeNormal, "RestartRequested",
		"Asked the node agent on %s to schedule a detached restart of %s.service", node.Name, unit)
	ns.Phase = framev1beta1.PhaseApplying
	ns.Message = fmt.Sprintf("drained; restart of %s.service requested", unit)
	return ns
}

// failRollout records the failure on the node's status entry and on the node
// itself, and leaves the node cordoned with its rollout annotations intact —
// which is what halts every other node, since the campaign lock keys on
// exactly that.
func (r *NodeTuningReconciler) failRollout(
	ctx context.Context,
	nt *framev1beta1.NodeTuning,
	node *corev1.Node,
	ns framev1beta1.NodeTuningNodeStatus,
	format string,
	args ...any,
) framev1beta1.NodeTuningNodeStatus {
	msg := fmt.Sprintf(format, args...)
	log := logf.FromContext(ctx)
	log.Info("NodeTuning rollout failed", "node", node.Name, "reason", msg)

	// The marker is what freezes this node on later passes. If the patch
	// fails the node is simply re-evaluated next time and fails again the
	// same way — the deadline that produced this failure has not moved.
	if err := r.patchNode(ctx, node, func(n *corev1.Node) {
		setNodeAnnotation(n, framev1beta1.TuningRolloutFailedAnnotation, time.Now().Format(time.RFC3339Nano))
	}); err != nil {
		log.Error(err, "marking the rollout failed", "node", node.Name)
	}

	r.Recorder.Eventf(nt, corev1.EventTypeWarning, "RestartFailed", "%s: %s", node.Name, msg)
	ns.Phase = framev1beta1.PhaseFailed
	ns.Message = msg
	return ns
}

// evictPods requests eviction for every pod on nodeName that a drain should
// move, and reports how many are still there. Eviction rather than delete is
// the entire point: the eviction API is what enforces PodDisruptionBudgets,
// and a budget refusing one is the budget working, not an error.
func (r *NodeTuningReconciler) evictPods(ctx context.Context, nodeName string) (remaining int, err error) {
	log := logf.FromContext(ctx)

	var pods corev1.PodList
	if err := r.reader().List(ctx, &pods, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return 0, fmt.Errorf("listing pods on %s: %w", nodeName, err)
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if !blocksDrain(pod) {
			continue
		}
		remaining++
		if pod.DeletionTimestamp != nil {
			// Already evicted, still going. Asking again would just add API
			// calls to a pod that is on its way out.
			continue
		}
		ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}}
		if err := r.SubResource("eviction").Create(ctx, pod, ev); err != nil {
			// Deliberately not returned: the common case is a
			// PodDisruptionBudget saying "not this one, not yet" (429), which
			// is exactly what respecting budgets looks like from here. The
			// drain deadline is what eventually turns a permanent refusal
			// into a failure.
			log.Info("eviction refused, will retry", "pod", client.ObjectKeyFromObject(pod).String(), "error", err.Error())
		}
	}
	return remaining, nil
}

// blocksDrain reports whether pod is one a drain has to wait for.
func blocksDrain(pod *corev1.Pod) bool {
	// Already finished: nothing to move, and nothing a restart can disturb.
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
	// Static/mirror pods are owned by the node's own kubelet manifests; the
	// API server copy cannot be evicted, and waiting for it never ends.
	if _, ok := pod.Annotations[corev1.MirrorPodAnnotationKey]; ok {
		return false
	}
	// DaemonSet pods are recreated on the same node by design, so waiting for
	// them to leave waits forever — and the node agent, which is what has to
	// service the restart request, is one of them.
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return false
		}
	}
	return true
}
