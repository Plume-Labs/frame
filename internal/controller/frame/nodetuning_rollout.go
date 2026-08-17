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
// change is approved for the live generation (Task 5's gate), it cordons the
// node, evicts its pods through the eviction API so PodDisruptionBudgets are
// honoured, asks the node agent to schedule a detached restart, waits for the
// unit to come back, and uncordons.
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

// reader returns the client the drain lists pods with. See APIReader's doc.
func (r *NodeTuningReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// rolloutInFlight reports whether this node is mid-rollout (or stuck in a
// failed one). It is deliberately the presence of an annotation the
// controller itself wrote, not a phase in some NodeTuning's status: it is the
// one piece of state that is per-node, survives the controller restarting,
// and is visible to every NodeTuning at once — which is exactly what "one
// node at a time, cluster-wide" and "a failure halts the campaign" both need.
// Clearing it by hand is also how an operator releases a halted campaign.
func rolloutInFlight(node *corev1.Node) bool {
	_, ok := node.Annotations[framev1beta1.TuningRolloutStartedAnnotation]
	return ok
}

// otherNodeInRollout names a node other than exclude that is mid-rollout or
// left cordoned by a failed one, or "" if none is.
func otherNodeInRollout(nodes []corev1.Node, exclude string) string {
	for i := range nodes {
		if nodes[i].Name == exclude {
			continue
		}
		if rolloutInFlight(&nodes[i]) {
			return nodes[i].Name
		}
	}
	return ""
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
func restartedForGeneration(ns framev1beta1.NodeTuningNodeStatus, generation int64) bool {
	return ns.RestartedAt != nil && ns.AppliedGeneration == generation
}

// patchNode applies mutate to a copy of node and patches the difference,
// keeping every node write a merge patch rather than a full update that could
// clobber a concurrent writer (the agent writes annotations on this same
// object every tick).
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
) framev1beta1.NodeTuningNodeStatus {
	log := logf.FromContext(ctx)

	if other := otherNodeInRollout(allNodes, node.Name); other != "" {
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

	// No baseline, no verification: without the unit's current
	// ActiveEnterTimestamp there is nothing a later reading could be compared
	// against, so "did it come back" would be unanswerable. Cordoning and
	// draining for a restart that can never be verified is strictly worse
	// than waiting for the agent to report.
	baseline, err := annotationTime(node, framev1beta1.TuningUnitActiveEnterAnnotation)
	if err != nil {
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf("waiting for a verifiable baseline: %v", err)
		return ns
	}

	now := time.Now()
	if err := r.patchNode(ctx, node, func(n *corev1.Node) {
		n.Spec.Unschedulable = true
		setNodeAnnotation(n, framev1beta1.TuningRolloutStartedAnnotation, now.Format(time.RFC3339Nano))
		setNodeAnnotation(n, framev1beta1.TuningRestartBaselineAnnotation, baseline.Format(time.RFC3339Nano))
	}); err != nil {
		// Tolerated and retried, not failed: nothing has happened yet.
		log.Error(err, "cordoning node for tuning restart", "node", node.Name)
		ns.Phase = framev1beta1.PhaseRebootPending
		ns.Message = fmt.Sprintf("cordoning %s: %v", node.Name, err)
		return ns
	}

	r.Recorder.Eventf(nt, corev1.EventTypeNormal, "NodeCordoned",
		"Cordoned %s to restart %s.service: %s", node.Name, unit, reason)
	ns.Phase = framev1beta1.PhaseApplying
	ns.Message = fmt.Sprintf("cordoned; draining before restarting %s.service (%s)", unit, reason)
	return ns
}

// continueRollout drives a node that is already cordoned: drain, then ask,
// then wait. It is called on every reconcile of a node carrying the rollout
// annotation, whatever the diff currently says — a node this controller
// cordoned must be uncordoned by this controller, even if the desired state
// changed underneath it in the meantime.
func (r *NodeTuningReconciler) continueRollout(
	ctx context.Context,
	nt *framev1beta1.NodeTuning,
	node *corev1.Node,
	ns framev1beta1.NodeTuningNodeStatus,
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
					"%s.service restarted at %s but %s never reported Ready within %s; leaving it cordoned",
					unit, current.Format(time.RFC3339), node.Name, r.restartTimeout())
			}
			ns.Phase = framev1beta1.PhaseApplying
			ns.Message = fmt.Sprintf("%s.service restarted at %s; waiting for %s to report Ready before uncordoning",
				unit, current.Format(time.RFC3339), node.Name)
			return ns
		}

		if err := r.patchNode(ctx, node, func(n *corev1.Node) {
			n.Spec.Unschedulable = false
			delete(n.Annotations, framev1beta1.TuningRolloutStartedAnnotation)
			delete(n.Annotations, framev1beta1.TuningRestartBaselineAnnotation)
			delete(n.Annotations, framev1beta1.TuningRestartRequestedAnnotation)
		}); err != nil {
			// Tolerated and retried: the restart is verified either way, and
			// re-running this branch is harmless.
			log.Error(err, "uncordoning node after tuning restart", "node", node.Name)
			ns.Phase = framev1beta1.PhaseApplying
			ns.Message = fmt.Sprintf("uncordoning %s: %v", node.Name, err)
			return ns
		}

		r.Recorder.Eventf(nt, corev1.EventTypeNormal, "RestartVerified",
			"%s.service on %s restarted at %s; node uncordoned", unit, node.Name, current.Format(time.RFC3339))
		// RestartedAt is the unit's own ActiveEnterTimestamp rather than "when
		// the controller noticed": Task 7 compares pod start times against it,
		// and a pod started between the real restart and this reconcile did
		// inherit the new setting.
		restartedAt := metav1.NewTime(current)
		ns.RestartedAt = &restartedAt
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
			"%s.service on %s did not come back within %s of the restart request: %s; leaving it cordoned and halting the rollout",
			unit, node.Name, r.restartTimeout(), detail)
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
				"draining %s did not finish within %s (%d pod(s) still there); leaving it cordoned and halting the rollout",
				node.Name, r.drainTimeout(), remaining)
		}
		ns.Phase = framev1beta1.PhaseApplying
		ns.Message = fmt.Sprintf("draining: %d pod(s) still on %s", remaining, node.Name)
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

// failRollout records the failure on the node's status entry and leaves the
// node cordoned with its rollout annotations intact — which is what halts
// every other node, since otherNodeInRollout keys on exactly that.
func (r *NodeTuningReconciler) failRollout(
	ctx context.Context,
	nt *framev1beta1.NodeTuning,
	node *corev1.Node,
	ns framev1beta1.NodeTuningNodeStatus,
	format string,
	args ...any,
) framev1beta1.NodeTuningNodeStatus {
	msg := fmt.Sprintf(format, args...)
	logf.FromContext(ctx).Info("NodeTuning rollout failed", "node", node.Name, "reason", msg)
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
