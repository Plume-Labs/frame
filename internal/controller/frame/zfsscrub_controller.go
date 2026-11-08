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
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // the schedule's zone, from the binary: the image has no zone database

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// ZFSScrubReconciler plans the ZFS scrubs StorageHealthPolicy.spec.scrub
// asks for: one node at a time, cluster-wide, started inside the window.
//
// It never runs anything on a node. It writes a request timestamp onto the
// Node (framev1beta1.ZFSScrubRequestedAnnotation); the node agent starts the
// scrub on the pools it reads itself and acknowledges
// (internal/agent/zfsscrub.go). The scrub's progress and result come back
// through the agent's storage read on the node's FrameMachine
// (status.storage.local.zfs), the same read T-1's ScrubErrors check judges.
//
// It polls rather than watching Nodes and FrameMachines: the agent rewrites
// every FrameMachine's status each tick, and a scrub plan measured in days
// has no use for a reconcile every few seconds.
type ZFSScrubReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// APIReader reads the Nodes straight from the API server. The plan's one
	// guarantee is that no two nodes hold a request, and a cache that has not
	// yet seen the request it wrote two minutes ago would hand out a second
	// one. nil falls back to Client (tests whose client is not cached).
	APIReader client.Reader
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

const (
	// zfsScrubPoll is how often the plan is re-read.
	zfsScrubPoll = 2 * time.Minute
	// zfsScrubAckTimeout is how long the agent has to take a request.
	zfsScrubAckTimeout = 10 * time.Minute
	// zfsScrubReadTimeout is how long, after the agent started a scrub, the
	// controller waits for a storage read dated after that start. Past it the
	// agent has stopped reporting, and the lock is released rather than held
	// on a node nobody hears from.
	zfsScrubReadTimeout = 30 * time.Minute
	// zfsScrubRetryAfterFailure is how long a node whose request failed is
	// left alone.
	zfsScrubRetryAfterFailure = 24 * time.Hour
)

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=storagehealthpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *ZFSScrubReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *ZFSScrubReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile reads the policy, the nodes and their machines, and applies the
// plan's node patches.
func (r *ZFSScrubReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if req.Name != framev1beta1.StorageHealthPolicyName {
		return ctrl.Result{}, nil
	}
	var policy framev1beta1.StorageHealthPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var nodes corev1.NodeList
	if err := r.reader().List(ctx, &nodes); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing nodes: %w", err)
	}
	var machines framev1beta1.FrameMachineList
	if err := r.List(ctx, &machines); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing FrameMachines: %w", err)
	}
	zfsOf := map[string]*framev1beta1.ZFSStatus{}
	for i := range machines.Items {
		m := &machines.Items[i]
		if m.Spec.NodeRef != "" && m.Status.Storage != nil && m.Status.Storage.Local != nil {
			zfsOf[m.Spec.NodeRef] = &m.Status.Storage.Local.ZFS
		}
	}
	in := make([]scrubNode, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		in = append(in, scrubNode{Name: n.Name, Labels: n.Labels, Annotations: n.Annotations, ZFS: zfsOf[n.Name]})
	}

	steps, err := planScrubs(policy.Spec.Scrub, in, r.now())
	if err != nil {
		r.Recorder.Event(&policy, corev1.EventTypeWarning, "ScrubScheduleInvalid", err.Error())
	}
	for _, s := range steps {
		if err := r.patchNode(ctx, s); err != nil {
			if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
				// The agent acknowledged in between, or the node left: the
				// next pass plans from what is there now.
				log.Info("scrub step skipped, node changed", "node", s.Node, "error", err.Error())
				continue
			}
			return ctrl.Result{}, err
		}
		if s.Event != "" {
			r.Recorder.Event(&policy, s.EventType, s.Reason, s.Event)
		}
	}
	return ctrl.Result{RequeueAfter: zfsScrubPoll}, nil
}

func (r *ZFSScrubReconciler) patchNode(ctx context.Context, s scrubStep) error {
	var node corev1.Node
	if err := r.reader().Get(ctx, types.NamespacedName{Name: s.Node}, &node); err != nil {
		return err
	}
	// The plan was made from the annotations it read; a node whose scrub
	// annotations moved since (the agent acknowledged, a human cleared one)
	// is left for the next pass, which reads the new ones. Read from the API
	// server, like the plan's list, then written under its resourceVersion.
	for _, k := range scrubAnnotations {
		if node.Annotations[k] != s.Seen[k] {
			return apierrors.NewConflict(corev1.Resource("nodes"), s.Node, fmt.Errorf("annotation %s moved", k))
		}
	}
	base := node.DeepCopy()
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	maps.Copy(node.Annotations, s.Set)
	for _, k := range s.Remove {
		delete(node.Annotations, k)
	}
	return r.Patch(ctx, &node, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// SetupWithManager sets up the controller with the Manager.
func (r *ZFSScrubReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.StorageHealthPolicy{}).
		Named("zfsscrub").
		Complete(r)
}

// Pool health a scrub runs under, and the step reasons (also the events').
const (
	poolOnline   = "ONLINE"
	poolDegraded = "DEGRADED"

	reasonScrubRequested = "ScrubRequested"
	reasonScrubCompleted = "ScrubCompleted"
	reasonScrubFailed    = "ScrubFailed"
	reasonScrubSettled   = "ScrubSettled"
)

// scrubAnnotations is every annotation of the protocol.
var scrubAnnotations = []string{
	framev1beta1.ZFSScrubRequestedAnnotation,
	framev1beta1.ZFSScrubAcknowledgedAnnotation,
	framev1beta1.ZFSScrubStartedAtAnnotation,
	framev1beta1.ZFSScrubCompletedAnnotation,
	framev1beta1.ZFSScrubFailedAnnotation,
	framev1beta1.ZFSScrubUnsettledAnnotation,
}

// scrubNode is what the plan knows of one node.
type scrubNode struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	// ZFS is the node's FrameMachine's read; nil when no machine claims the
	// node or the agent has not read it yet.
	ZFS *framev1beta1.ZFSStatus
}

// scrubStep is one node patch, with the annotations it was planned from.
type scrubStep struct {
	Node      string
	Seen      map[string]string
	Set       map[string]string
	Remove    []string
	EventType string
	Reason    string
	Event     string
}

// scanning reports whether a pool of zfs is being scrubbed or resilvered.
// The scan line is zpool's own ("scrub in progress since …").
func scanning(zfs *framev1beta1.ZFSStatus) bool {
	if zfs == nil {
		return false
	}
	return slices.ContainsFunc(zfs.Pools, func(p framev1beta1.ZPool) bool {
		return strings.Contains(p.Scan, "in progress")
	})
}

// blocking reports whether a node keeps every other node from being asked:
// a pool scanning, a ZFS read that failed (its lists are empty, so a
// resilver there would not show), or a failed request not yet settled.
func blocking(n scrubNode) bool {
	if scanning(n.ZFS) || (n.ZFS != nil && n.ZFS.State == framev1beta1.StackError) {
		return true
	}
	return unsettled(n)
}

// unsettled reports whether a failed request may still have a scrub running
// on the node: the failure released the request, not the pools. It holds
// until the agent publishes a complete read dated after the last read seen
// at the failure (the same clock, the agent's) that shows no scan.
func unsettled(n scrubNode) bool {
	raw, ok := n.Annotations[framev1beta1.ZFSScrubUnsettledAnnotation]
	if !ok {
		return false
	}
	if n.ZFS == nil || n.ZFS.State != framev1beta1.StackPresent || n.ZFS.ReadAt == nil {
		return true
	}
	if seen, ok := parseStamp(raw); ok && !n.ZFS.ReadAt.After(seen) {
		return true
	}
	return scanning(n.ZFS)
}

// finished reports whether every scrubbable pool's scan line is a completed
// scrub ("scrub repaired … with N errors on …").
func finished(zfs *framev1beta1.ZFSStatus) bool {
	found := false
	for _, p := range zfs.Pools {
		if p.Health != poolOnline && p.Health != poolDegraded {
			continue
		}
		if !strings.Contains(p.Scan, "scrub repaired") {
			return false
		}
		found = true
	}
	return found
}

// scrubbable reports whether the agent would find a pool to scrub.
func scrubbable(zfs *framev1beta1.ZFSStatus) bool {
	return zfs != nil && zfs.State == framev1beta1.StackPresent &&
		slices.ContainsFunc(zfs.Pools, func(p framev1beta1.ZPool) bool {
			return p.Health == poolOnline || p.Health == poolDegraded
		})
}

func parseStamp(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	return t, err == nil
}

func seenOf(n scrubNode) map[string]string {
	seen := map[string]string{}
	for _, k := range scrubAnnotations {
		if v, ok := n.Annotations[k]; ok {
			seen[k] = v
		}
	}
	return seen
}

// inScrubWindow reports whether now falls in the start window.
func inScrubWindow(s *framev1beta1.ZFSScrubSchedule, now time.Time) (bool, error) {
	tzName := s.TimeZone
	if tzName == "" {
		tzName = "UTC"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return false, fmt.Errorf("scrub timeZone %q: %w", tzName, err)
	}
	start := s.WindowStart
	if start == "" {
		start = "02:00"
	}
	at, err := time.Parse("15:04", start)
	if err != nil {
		return false, fmt.Errorf("scrub windowStart %q: %w", start, err)
	}
	hours := s.WindowHours
	if hours <= 0 {
		hours = 4
	}
	local := now.In(loc)
	minutes := local.Hour()*60 + local.Minute()
	since := (minutes - (at.Hour()*60 + at.Minute()) + 24*60) % (24 * 60)
	return since < int(hours)*60, nil
}

// planScrubs decides the node patches of one pass. In-flight requests are
// concluded first, whatever the schedule now says (a request outlives a
// selector edit); a new request is issued only when no node holds one, no
// pool anywhere is being scanned, and now is in the window.
func planScrubs(s *framev1beta1.ZFSScrubSchedule, nodes []scrubNode, now time.Time) ([]scrubStep, error) {
	var steps []scrubStep
	locked := false
	for _, n := range nodes {
		if _, ok := n.Annotations[framev1beta1.ZFSScrubUnsettledAnnotation]; ok && !unsettled(n) {
			steps = append(steps, scrubStep{
				Node: n.Name, Seen: seenOf(n), Remove: []string{framev1beta1.ZFSScrubUnsettledAnnotation},
				EventType: corev1.EventTypeNormal, Reason: reasonScrubSettled,
				Event: fmt.Sprintf("node %s: a fresh read shows no scrub left running after the failure", n.Name),
			})
			continue
		}
		if n.Annotations[framev1beta1.ZFSScrubRequestedAnnotation] == "" {
			continue
		}
		if step, ok := concludeScrub(n, now); ok {
			steps = append(steps, step)
		} else {
			locked = true
		}
	}
	// A node concluded in this pass is not asked again in the same pass: the
	// next one, minutes later, plans from the annotations just written.
	if s == nil || locked || len(steps) > 0 ||
		slices.ContainsFunc(nodes, blocking) {
		return steps, nil
	}
	open, err := inScrubWindow(s, now)
	if err != nil || !open || s.NodeSelector == nil {
		return steps, err
	}
	pick, ok, err := nextScrub(s, nodes, now)
	if err != nil || !ok {
		return steps, err
	}
	return append(steps, scrubStep{
		Node: pick.Name, Seen: seenOf(pick),
		Set:       map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: now.UTC().Format(time.RFC3339Nano)},
		EventType: corev1.EventTypeNormal, Reason: reasonScrubRequested,
		Event: fmt.Sprintf("node %s: scrub requested", pick.Name),
	}), nil
}

// concludeScrub ends the request n holds, when it can be ended: the scrub
// finished, or the request failed. false means it is still in flight, which
// holds the cluster-wide lock.
func concludeScrub(n scrubNode, now time.Time) (scrubStep, bool) {
	clear := []string{framev1beta1.ZFSScrubRequestedAnnotation, framev1beta1.ZFSScrubAcknowledgedAnnotation, framev1beta1.ZFSScrubStartedAtAnnotation}
	fail := func(why string) (scrubStep, bool) {
		set := map[string]string{framev1beta1.ZFSScrubFailedAnnotation: now.UTC().Format(time.RFC3339Nano)}
		// The scrub may be running all the same (started, then the agent
		// went silent): the cluster-wide lock stays until a read after this
		// one says otherwise. "none" when there was no read at all.
		set[framev1beta1.ZFSScrubUnsettledAnnotation] = "none"
		if n.ZFS != nil && n.ZFS.ReadAt != nil {
			set[framev1beta1.ZFSScrubUnsettledAnnotation] = n.ZFS.ReadAt.UTC().Format(time.RFC3339Nano)
		}
		return scrubStep{
			Node: n.Name, Seen: seenOf(n), Remove: clear, Set: set,
			EventType: corev1.EventTypeWarning, Reason: reasonScrubFailed,
			Event: fmt.Sprintf("node %s: %s; not asked again for %s", n.Name, why, zfsScrubRetryAfterFailure),
		}, true
	}
	raw := n.Annotations[framev1beta1.ZFSScrubRequestedAnnotation]
	requested, ok := parseStamp(raw)
	if !ok {
		return fail("unreadable scrub request " + raw)
	}
	if n.Annotations[framev1beta1.ZFSScrubAcknowledgedAnnotation] != raw {
		if now.Sub(requested) > zfsScrubAckTimeout {
			return fail(fmt.Sprintf("the agent did not start the scrub within %s (see its log)", zfsScrubAckTimeout))
		}
		return scrubStep{}, false
	}
	startedAt, ok := parseStamp(n.Annotations[framev1beta1.ZFSScrubStartedAtAnnotation])
	if !ok {
		return fail("acknowledged with no readable start time")
	}
	// A read that failed (error) has empty lists, and a busy one carries the
	// last complete read: neither says the scrub ended.
	fresh := n.ZFS != nil && n.ZFS.State == framev1beta1.StackPresent &&
		n.ZFS.ReadAt != nil && n.ZFS.ReadAt.After(startedAt)
	switch {
	case fresh && !scanning(n.ZFS) && !finished(n.ZFS):
		// Stopped (`zpool scrub -s`), paused, or no scrub at all: recorded
		// as a failure, so the interval does not restart on a scrub that
		// never ran to the end.
		return fail("the scrub ended without completing on every pool (stopped or paused?)")
	case fresh && !scanning(n.ZFS):
		return scrubStep{
			Node: n.Name, Seen: seenOf(n), Remove: clear,
			Set:       map[string]string{framev1beta1.ZFSScrubCompletedAnnotation: now.UTC().Format(time.RFC3339Nano)},
			EventType: corev1.EventTypeNormal, Reason: reasonScrubCompleted,
			Event: fmt.Sprintf("node %s: scrub finished; ScrubErrors on its FrameMachine carries the result", n.Name),
		}, true
	case !fresh && now.Sub(requested) > zfsScrubAckTimeout+zfsScrubReadTimeout:
		// Measured from the request, the controller's own clock: the start
		// is the agent's, and a skew between the two must not fail it early.
		return fail(fmt.Sprintf("no storage read since the scrub started (requested %s ago)", now.Sub(requested).Round(time.Minute)))
	}
	return scrubStep{}, false
}

// nextScrub picks the due node: selected, with a pool to scrub, not failed
// in the last day, its last completed scrub older than the interval. Never
// scrubbed by Frame comes first, then the oldest, then by name.
func nextScrub(s *framev1beta1.ZFSScrubSchedule, nodes []scrubNode, now time.Time) (scrubNode, bool, error) {
	sel, err := metav1.LabelSelectorAsSelector(s.NodeSelector)
	if err != nil {
		return scrubNode{}, false, fmt.Errorf("scrub nodeSelector: %w", err)
	}
	days := s.IntervalDays
	if days <= 0 {
		days = 30 // the CRD's default, for an object written before it
	}
	interval := time.Duration(days) * 24 * time.Hour
	type candidate struct {
		node scrubNode
		last time.Time // zero when never completed
	}
	var due []candidate
	for _, n := range nodes {
		if !sel.Matches(labels.Set(n.Labels)) || !scrubbable(n.ZFS) {
			continue
		}
		if failed, ok := parseStamp(n.Annotations[framev1beta1.ZFSScrubFailedAnnotation]); ok && now.Sub(failed) < zfsScrubRetryAfterFailure {
			continue
		}
		last, ok := parseStamp(n.Annotations[framev1beta1.ZFSScrubCompletedAnnotation])
		if ok && now.Sub(last) < interval {
			continue
		}
		due = append(due, candidate{node: n, last: last})
	}
	if len(due) == 0 {
		return scrubNode{}, false, nil
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].last.Equal(due[j].last) {
			return due[i].last.Before(due[j].last)
		}
		return due[i].node.Name < due[j].node.Name
	})
	return due[0].node, true, nil
}
