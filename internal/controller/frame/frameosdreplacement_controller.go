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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/cephmgr"
)

// The OSD replacement: one OSD's disk swapped for a new one, end to end. The
// steps and their guards follow the procedure audited for the R320 swap of
// 13/11 (fiche r320-osd and its script), which stays the fallback:
//
//	Checking               read-only: nothing else running, health OK (or
//	                       only the tolerated warnings), every PG
//	                       active+clean, every OSD up/in, no blocking flag,
//	                       ok-to-stop (see cephmgr.OKToStopVerdict), the
//	                       retyped serial is the OSD's, the old disk and
//	                       every OSD of the host are declared in the
//	                       CephCluster
//	Draining               osd out, wait for active+clean, safe-to-destroy
//	AwaitingPurgeApproval  approval no. 1: the old disk's serial retyped
//	Purging                deployment to 0, OSD down, safe-to-destroy again,
//	                       purge (sent once), deployment deleted
//	AwaitingDisk           the person swaps the disk; exactly one new disk
//	                       (absent before the purge, unknown to Ceph,
//	                       whole and free, 512/4096-byte sectors, SMART not
//	                       failed) must appear
//	AwaitingWipeApproval   approval no. 2: the new disk's serial retyped
//	Wiping                 the node's agent wipes it (internal/agent)
//	Declaring              JSON patch test+replace of that one entry of
//	                       spec.storage.devices — never an apply
//	AwaitingOSD            the host has as many OSDs as before, all up
//	                       (operator restarted once after 10 min)
//	Backfilling            every PG active+clean again
//
// Every refusal pauses (phase Paused, with the reason); spec.resume retries
// the step. Reads that fail (network, dashboard) are waited out, never a
// reason to act. The purge is sent at most once per object; a wipe at most
// once per request id (the agent records it).

// toleratedHealthChecks are the warnings a replacement starts under: osd.0's
// slow BlueStore ops, known and tolerated (spec of 08/11, recommendation 4).
var toleratedHealthChecks = []string{"BLUESTORE_SLOW_OP_ALERT"}

// blockingFlags are the cluster flags under which a replacement does not
// start: each would stop the drain or the backfill it waits for.
var blockingFlags = []string{"noout", "noin", "noup", "nodown", "norebalance", "nobackfill", "norecover", "pause", "pauserd", "pausewr"}

const (
	osdReplacementPoll      = 30 * time.Second
	osdMinMovementWait      = 2 * time.Minute
	osdScaleDownTimeout     = 3 * time.Minute
	osdPurgeVisibleWithin   = 1 * time.Minute
	osdWipeAnswerTimeout    = 10 * time.Minute
	osdOperatorRestartAfter = 10 * time.Minute
	osdNewOSDTimeout        = 20 * time.Minute
	maxOSDReplacementEvents = 64
)

// CephOperator is what the replacement asks of Ceph (cephmgr.Operator).
type CephOperator interface {
	Status(ctx context.Context) (*cephmgr.ClusterStatus, error)
	OSDs(ctx context.Context) ([]cephmgr.OSDState, error)
	Metadata(ctx context.Context, id int32) (*cephmgr.OSDMetadata, error)
	Flags(ctx context.Context) ([]string, error)
	PoolSizes(ctx context.Context) ([]cephmgr.Pool, error)
	SafeToDestroy(ctx context.Context, id int32) (bool, string, error)
	MarkOut(ctx context.Context, id int32) error
	MarkIn(ctx context.Context, id int32) error
	Purge(ctx context.Context, id int32) error
}

// FrameOSDReplacementReconciler drives FrameOSDReplacement objects.
type FrameOSDReplacementReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Ceph returns the operator-account client, or an error when it is not
	// configured (cephmgr.ErrNotConfigured) or cannot be built.
	Ceph func(ctx context.Context) (CephOperator, error)
	// RookNamespace and CephClusterName locate the CephCluster and the OSD
	// deployments.
	RookNamespace   string
	CephClusterName string
	Now             func() time.Time
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameosdreplacements,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameosdreplacements/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameosdreplacements/finalizers,verbs=update
// +kubebuilder:rbac:groups=ceph.rook.io,resources=cephclusters,verbs=get;patch,resourceNames=rook-ceph
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=list
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines,verbs=get;list

func (r *FrameOSDReplacementReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func osdTerminal(phase string) bool {
	return phase == framev1beta1.OSDReplacementSucceeded || phase == framev1beta1.OSDReplacementCancelled || phase == framev1beta1.OSDReplacementFailed
}

// step is one reconcile of one phase: o is mutated in memory, saved by
// Reconcile from base.
type osdStep struct {
	r    *FrameOSDReplacementReconciler
	o    *framev1beta1.FrameOSDReplacement
	ceph CephOperator
	// base is the object as last stored: every status patch (the
	// write-ahead saves and Reconcile's final one) is computed from it and
	// carries its resourceVersion, then base moves forward.
	base *framev1beta1.FrameOSDReplacement
}

func (r *FrameOSDReplacementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var o framev1beta1.FrameOSDReplacement
	if err := r.Get(ctx, req.NamespacedName, &o); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if osdTerminal(o.Status.Phase) {
		return ctrl.Result{}, nil
	}
	s := &osdStep{r: r, o: &o, base: o.DeepCopy()}
	res, err := s.reconcile(ctx)
	if !statusEqual(s.base, &o) {
		// Optimistic: a reconcile working from a stale copy must not
		// replace what a newer one stored (the journal included).
		if perr := r.Status().Patch(ctx, &o, client.MergeFromWithOptions(s.base, client.MergeFromWithOptimisticLock{})); perr != nil {
			return ctrl.Result{}, errors.Join(err, perr)
		}
	}
	return res, err
}

func statusEqual(a, b *framev1beta1.FrameOSDReplacement) bool {
	ja, _ := json.Marshal(a.Status)
	jb, _ := json.Marshal(b.Status)
	return string(ja) == string(jb)
}

// save writes the status now: a write-ahead record before a call that
// changes Ceph or a disk. It carries every change made in memory so far,
// and the resourceVersion of the object as last stored: a reconcile
// working from a stale copy cannot pass the guards and record (then send)
// a call twice. On failure the in-memory status goes back to what is
// stored, so that nothing records a call that was never sent.
func (s *osdStep) save(ctx context.Context) error {
	if err := s.r.Status().Patch(ctx, s.o, client.MergeFromWithOptions(s.base, client.MergeFromWithOptimisticLock{})); err != nil {
		s.base.Status.DeepCopyInto(&s.o.Status)
		return err
	}
	s.base = s.o.DeepCopy()
	return nil
}

func (s *osdStep) reconcile(ctx context.Context) (ctrl.Result, error) {
	r, o := s.r, s.o
	if o.Status.Phase == "" {
		s.enter(framev1beta1.OSDReplacementChecking, fmt.Sprintf("replacement of osd.%d requested", o.Spec.OSDID))
	}
	if o.Spec.Cancel {
		return s.cancel(ctx)
	}
	if o.Status.Phase == framev1beta1.OSDReplacementPaused {
		if o.Spec.Resume <= o.Status.ObservedResume {
			return ctrl.Result{}, nil
		}
		phase := o.Status.PausedPhase
		if phase == framev1beta1.OSDReplacementWiping && o.Status.WipedAt == nil {
			// Back to approval no. 2, like the purge: a new attempt is a
			// new approval and a new request id (the agent wipes each id
			// once, and checks the disk again from scratch).
			o.Status.WipeRequestID, o.Status.WipeRequestedAt, o.Status.WipeApprovedAt = "", nil, nil
			phase = framev1beta1.OSDReplacementAwaitingWipeApproval
		}
		if phase == framev1beta1.OSDReplacementPurging && o.Status.PurgeRequestedAt == nil {
			// The cluster moved enough to pause before the purge was sent:
			// the approval given before that does not count any more. The
			// OSD goes back up (still out) until the next approval.
			if o.Status.ScaledDownAt != nil {
				if err := s.scaleOSD(ctx, 1); err != nil && !apierrors.IsNotFound(err) {
					return s.wait(fmt.Sprintf("resuming: scaling %s back to 1: %v", s.osdDeployment().Name, err))
				}
				o.Status.ScaledDownAt = nil
			}
			o.Status.PurgeApprovedAt = nil
			phase = framev1beta1.OSDReplacementAwaitingPurgeApproval
		}
		// Counted only now: a resume that had to wait above is retried.
		o.Status.ObservedResume = o.Spec.Resume
		o.Status.PausedPhase = ""
		s.enter(phase, "resumed by a person")
		// The step runs on the next read, where the phase is stored: an
		// approval step then arms on a generation read after it.
		return ctrl.Result{Requeue: true}, nil
	}
	op, err := r.Ceph(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("the Ceph operator account is not usable: %v", err))
	}
	s.ceph = op
	switch o.Status.Phase {
	case framev1beta1.OSDReplacementChecking:
		return s.check(ctx)
	case framev1beta1.OSDReplacementDraining:
		return s.drain(ctx)
	case framev1beta1.OSDReplacementAwaitingPurgeApproval:
		return s.awaitPurgeApproval(ctx)
	case framev1beta1.OSDReplacementPurging:
		return s.purge(ctx)
	case framev1beta1.OSDReplacementAwaitingDisk:
		return s.awaitDisk(ctx)
	case framev1beta1.OSDReplacementAwaitingWipeApproval:
		return s.awaitWipeApproval(ctx)
	case framev1beta1.OSDReplacementWiping:
		return s.wipe(ctx)
	case framev1beta1.OSDReplacementDeclaring:
		return s.declare(ctx)
	case framev1beta1.OSDReplacementAwaitingOSD:
		return s.awaitOSD(ctx)
	case framev1beta1.OSDReplacementBackfilling:
		return s.backfill(ctx)
	}
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------- helpers

func (s *osdStep) event(msg string) {
	ev := framev1beta1.OSDReplacementEvent{At: metav1.NewTime(s.r.now()), Phase: s.o.Status.Phase, Message: clipString(msg, 512)}
	s.o.Status.Events = append(s.o.Status.Events, ev)
	if n := len(s.o.Status.Events); n > maxOSDReplacementEvents {
		s.o.Status.Events = s.o.Status.Events[n-maxOSDReplacementEvents:]
	}
}

func (s *osdStep) enter(phase, msg string) {
	now := metav1.NewTime(s.r.now())
	s.o.Status.Phase = phase
	s.o.Status.PhaseSince = &now
	s.o.Status.ApprovalArmedGeneration = 0
	s.o.Status.Message = clipString(msg, 1024)
	s.event(msg)
}

// wait keeps the phase and says why, polling again.
func (s *osdStep) wait(msg string) (ctrl.Result, error) {
	s.o.Status.Message = clipString(msg, 1024)
	return ctrl.Result{RequeueAfter: osdReplacementPoll}, nil
}

// pause stops the replacement on a refusal until a person resumes it.
func (s *osdStep) pause(msg string) (ctrl.Result, error) {
	phase := s.o.Status.Phase
	s.enter(framev1beta1.OSDReplacementPaused, "paused at "+phase+": "+msg)
	s.o.Status.PausedPhase = phase
	return ctrl.Result{}, nil
}

func (s *osdStep) fail(msg string) (ctrl.Result, error) {
	s.enter(framev1beta1.OSDReplacementFailed, msg)
	return ctrl.Result{}, nil
}

func since(t *metav1.Time, now time.Time) time.Duration {
	if t == nil {
		return 0
	}
	return now.Sub(t.Time)
}

func (s *osdStep) nowTime() *metav1.Time {
	t := metav1.NewTime(s.r.now())
	return &t
}

func (s *osdStep) drainTimeout() time.Duration {
	m := s.o.Spec.DrainTimeoutMinutes
	if m <= 0 {
		m = 120
	}
	return time.Duration(m) * time.Minute
}

func (s *osdStep) recordPGs(st *cephmgr.ClusterStatus) {
	s.o.Status.PGs = fmt.Sprintf("%d/%d", st.ActiveClean(), st.NumPGs)
	if !st.AllActiveClean() {
		s.o.Status.SawUnclean = true
	}
}

func findOSD(osds []cephmgr.OSDState, id int32) *cephmgr.OSDState {
	for i := range osds {
		if osds[i].ID == id {
			return &osds[i]
		}
	}
	return nil
}

func hostOSDs(osds []cephmgr.OSDState, host string) []cephmgr.OSDState {
	var out []cephmgr.OSDState
	for _, o := range osds {
		if o.Host == host {
			out = append(out, o)
		}
	}
	return out
}

// defaultRookName is Rook's default: the namespace of the operator and
// of the OSD deployments, and the CephCluster's name.
const defaultRookName = "rook-ceph"

var cephClusterGVK = schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephCluster"}

func (r *FrameOSDReplacementReconciler) rookNamespace() string {
	if r.RookNamespace != "" {
		return r.RookNamespace
	}
	return defaultRookName
}

func (r *FrameOSDReplacementReconciler) clusterName() string {
	if r.CephClusterName != "" {
		return r.CephClusterName
	}
	return defaultRookName
}

// deviceNames reads the CephCluster's spec.storage.devices names. It refuses
// a layout the replacement does not handle: per-node lists, or
// useAllDevices (Rook would take any new disk on its own).
func (r *FrameOSDReplacementReconciler) deviceNames(ctx context.Context) ([]string, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(cephClusterGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.rookNamespace(), Name: r.clusterName()}, u); err != nil {
		return nil, fmt.Errorf("reading CephCluster %s/%s: %w", r.rookNamespace(), r.clusterName(), err)
	}
	if all, _, _ := unstructured.NestedBool(u.Object, "spec", "storage", "useAllDevices"); all {
		return nil, errRefusal("the CephCluster has spec.storage.useAllDevices: true; the replacement only handles an explicit device list")
	}
	for _, f := range []string{"deviceFilter", "devicePathFilter"} {
		if v, _, _ := unstructured.NestedString(u.Object, "spec", "storage", f); v != "" {
			return nil, errRefusal("the CephCluster has spec.storage." + f + "; Rook could take a new disk on its own: the replacement only handles an explicit device list")
		}
	}
	if nodes, _, _ := unstructured.NestedSlice(u.Object, "spec", "storage", "nodes"); len(nodes) > 0 {
		return nil, errRefusal("the CephCluster lists devices per node (spec.storage.nodes); the replacement only handles spec.storage.devices")
	}
	devs, found, err := unstructured.NestedSlice(u.Object, "spec", "storage", "devices")
	if err != nil || !found {
		return nil, errRefusal("the CephCluster has no spec.storage.devices list")
	}
	out := make([]string, 0, len(devs))
	for i, d := range devs {
		m, ok := d.(map[string]any)
		if !ok {
			return nil, errRefusal(fmt.Sprintf("spec.storage.devices[%d] is not an object", i))
		}
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out, nil
}

// refusal is an answer that pauses, as opposed to a read that failed.
type refusal struct{ msg string }

func (e *refusal) Error() string  { return e.msg }
func errRefusal(msg string) error { return &refusal{msg} }

// orPause pauses on a refusal and waits on anything else.
func (s *osdStep) orPause(err error) (ctrl.Result, error) {
	var rf *refusal
	if errors.As(err, &rf) {
		return s.pause(rf.msg)
	}
	return s.wait(err.Error())
}

// machine returns the FrameMachine whose agent reports host's disks, with a
// report no older than observationMaxAge.
func (s *osdStep) machine(ctx context.Context, host string) (*framev1beta1.FrameMachine, error) {
	var list framev1beta1.FrameMachineList
	if err := s.r.List(ctx, &list, client.InNamespace(s.o.Namespace)); err != nil {
		return nil, fmt.Errorf("listing FrameMachines: %w", err)
	}
	var found []*framev1beta1.FrameMachine
	for i := range list.Items {
		if list.Items[i].Spec.NodeRef == host {
			found = append(found, &list.Items[i])
		}
	}
	if len(found) != 1 {
		return nil, errRefusal(fmt.Sprintf("%d FrameMachines in %s name node %s (spec.nodeRef); exactly one is needed to see its disks", len(found), s.o.Namespace, host))
	}
	m := found[0]
	if m.Status.Storage == nil || m.Status.Storage.ObservedAt == nil {
		return nil, errRefusal(fmt.Sprintf("FrameMachine %s has never reported its disks (is frame-agent running on %s?)", m.Name, host))
	}
	if age := since(m.Status.Storage.ObservedAt, s.r.now()); age > observationMaxAge {
		return nil, fmt.Errorf("FrameMachine %s's disk report is %s old; waiting for a fresh one", m.Name, age.Truncate(time.Second))
	}
	return m, nil
}

func diskBySerial(m *framev1beta1.FrameMachine, serial string) []framev1beta1.ObservedDisk {
	var out []framev1beta1.ObservedDisk
	for _, d := range m.Status.Storage.Observed {
		if serial != "" && d.SerialNumber == serial {
			out = append(out, d)
		}
	}
	return out
}

func replaced(d framev1beta1.ObservedDisk) *framev1beta1.ReplacedDisk {
	return &framev1beta1.ReplacedDisk{
		ByIDPath: d.ByIDPath, Serial: d.SerialNumber, WWN: d.WWN, Path: d.Path,
		SizeGB: d.SizeGB, Model: d.Model, LogicalSectorBytes: d.LogicalSectorBytes, SMART: d.SMART.DeepCopy(),
	}
}

// notMyTurn says why o must wait: one replacement at a time, cluster-wide;
// the oldest goes first, and a paused one still holds the turn (it may be
// mid-way).
func (r *FrameOSDReplacementReconciler) notMyTurn(ctx context.Context, o *framev1beta1.FrameOSDReplacement) (string, error) {
	var all framev1beta1.FrameOSDReplacementList
	if err := r.List(ctx, &all); err != nil {
		return "", err
	}
	for i := range all.Items {
		p := &all.Items[i]
		if p.UID == o.UID || osdTerminal(p.Status.Phase) {
			continue
		}
		older := p.CreationTimestamp.Before(&o.CreationTimestamp) ||
			(p.CreationTimestamp.Equal(&o.CreationTimestamp) && p.Namespace+"/"+p.Name < o.Namespace+"/"+o.Name)
		if older {
			return fmt.Sprintf("waiting: replacement %s/%s (osd.%d) is in progress; one at a time", p.Namespace, p.Name, p.Spec.OSDID), nil
		}
	}
	// A Ceph action sent from Storage → Ceph actions (out, in, restart)
	// whose outcome is not known yet: the checks would read a cluster
	// about to change.
	var acts framev1beta1.FrameCephActionList
	if err := r.List(ctx, &acts); err != nil {
		return "", err
	}
	if what := cephGestureInFlight(acts.Items); what != "" {
		return "waiting: the Ceph action " + what + " is in progress", nil
	}
	return "", nil
}

const (
	cephHealthOK   = "HEALTH_OK"
	cephHealthWarn = "HEALTH_WARN"
	cephHealthErr  = "HEALTH_ERR"
)

// deviceIndex is the one entry of devices that is path, or why there is
// not exactly one.
func deviceIndex(devices []string, path string) (int, string) {
	idx := -1
	for i, n := range devices {
		if n == path {
			if idx >= 0 {
				return -1, "is listed twice in the CephCluster"
			}
			idx = i
		}
	}
	if idx < 0 {
		return -1, "is not in the CephCluster's spec.storage.devices"
	}
	return idx, ""
}

// healthRefusal says why Ceph's health refuses the replacement, or "":
// HEALTH_OK, or HEALTH_WARN with only tolerated checks.
func healthRefusal(st *cephmgr.ClusterStatus) string {
	if st.Health == cephHealthOK {
		return ""
	}
	if st.Health == cephHealthErr {
		return "Ceph is HEALTH_ERR"
	}
	var other []string
	for _, c := range st.Checks {
		if !slices.Contains(toleratedHealthChecks, c.Name) {
			other = append(other, c.Name+" ("+c.Message+")")
		}
	}
	if st.Health != cephHealthWarn || len(other) > 0 {
		return fmt.Sprintf("Ceph is %s with warnings outside the tolerated list %v: %s", st.Health, toleratedHealthChecks, strings.Join(other, "; "))
	}
	return ""
}

// ---------------------------------------------------------------- Checking

func (s *osdStep) check(ctx context.Context) (ctrl.Result, error) {
	o, id := s.o, s.o.Spec.OSDID

	if why, err := s.r.notMyTurn(ctx, o); err != nil {
		return s.wait(fmt.Sprintf("listing replacements: %v", err))
	} else if why != "" {
		return s.wait(why)
	}

	st, err := s.ceph.Status(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading ceph status: %v", err))
	}
	s.recordPGs(st)
	if why := healthRefusal(st); why != "" {
		return s.pause(why)
	}
	osds, err := s.ceph.OSDs(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
	}
	me := findOSD(osds, id)
	if me == nil {
		return s.fail(fmt.Sprintf("osd.%d does not exist in Ceph", id))
	}
	if me.Host == "" {
		return s.pause(fmt.Sprintf("osd.%d is under no CRUSH host", id))
	}
	flags, err := s.ceph.Flags(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD flags: %v", err))
	}
	for _, f := range flags {
		if slices.Contains(blockingFlags, f) {
			return s.pause(fmt.Sprintf("the cluster flag %q is set", f))
		}
	}
	pools, err := s.ceph.PoolSizes(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the pools: %v", err))
	}
	if ok, why := cephmgr.OKToStopVerdict(st, osds, pools); !ok {
		return s.pause("osd." + fmt.Sprint(id) + " is not ok to stop: " + why)
	}
	meta, err := s.ceph.Metadata(ctx, id)
	if err != nil {
		return s.wait(fmt.Sprintf("reading osd.%d's metadata: %v", id, err))
	}
	if meta.Hostname != me.Host {
		return s.pause(fmt.Sprintf("osd.%d runs on %q but sits under CRUSH host %q", id, meta.Hostname, me.Host))
	}
	if len(meta.Serials) != 1 {
		return s.pause(fmt.Sprintf("osd.%d reports %d device serials %v; the replacement handles one disk per OSD", id, len(meta.Serials), meta.Serials))
	}
	if meta.Serials[0] != o.Spec.Serial {
		return s.fail(fmt.Sprintf("the serial typed (%s) is not osd.%d's disk (Ceph reports %s); nothing was done — create a new replacement with the right serial", o.Spec.Serial, id, meta.Serials[0]))
	}
	m, err := s.machine(ctx, me.Host)
	if err != nil {
		return s.orPause(err)
	}
	old := diskBySerial(m, o.Spec.Serial)
	if len(old) != 1 || old[0].ByIDPath == "" {
		return s.pause(fmt.Sprintf("FrameMachine %s reports %d disks with serial %s (with a by-id path); exactly one is needed", m.Name, len(old), o.Spec.Serial))
	}
	devices, err := s.r.deviceNames(ctx)
	if err != nil {
		return s.orPause(err)
	}
	idx, why := deviceIndex(devices, old[0].ByIDPath)
	if why != "" {
		return s.pause(fmt.Sprintf("%s (osd.%d's disk) %s", old[0].ByIDPath, id, why))
	}
	// Every OSD of the host must be declared: otherwise a disk Rook did
	// not get from the list could pass for the new one.
	host := hostOSDs(osds, me.Host)
	for _, h := range host {
		hm, err := s.ceph.Metadata(ctx, h.ID)
		if err != nil {
			return s.wait(fmt.Sprintf("reading osd.%d's metadata: %v", h.ID, err))
		}
		for _, serial := range hm.Serials {
			ds := diskBySerial(m, serial)
			if len(ds) != 1 || !slices.Contains(devices, ds[0].ByIDPath) {
				return s.pause(fmt.Sprintf("osd.%d (serial %s) on %s is not declared in the CephCluster by its by-id path", h.ID, serial, me.Host))
			}
		}
	}

	o.Status.Host = me.Host
	o.Status.MachineRef = m.Name
	o.Status.OldDisk = replaced(old[0])
	i32 := int32(idx)
	o.Status.DeviceIndex = &i32
	o.Status.HostOSDsBefore = int32(len(host))
	o.Status.DisksBefore = nil
	for _, d := range m.Status.Storage.Observed {
		if d.ByIDPath != "" {
			o.Status.DisksBefore = append(o.Status.DisksBefore, d.ByIDPath)
		}
	}
	s.event(fmt.Sprintf("checks passed: osd.%d on %s is %s (serial %s), entry %d of the CephCluster; %s PGs active+clean", id, me.Host, old[0].ByIDPath, o.Spec.Serial, idx, o.Status.PGs))
	s.enter(framev1beta1.OSDReplacementDraining, fmt.Sprintf("marking osd.%d out", id))
	return ctrl.Result{Requeue: true}, nil
}

// ---------------------------------------------------------------- Draining

func (s *osdStep) drain(ctx context.Context) (ctrl.Result, error) {
	o, id, now := s.o, s.o.Spec.OSDID, s.r.now()
	if o.Status.MarkedOutAt == nil {
		o.Status.MarkedOutAt = s.nowTime()
		o.Status.SawUnclean = false
		if err := s.save(ctx); err != nil {
			return ctrl.Result{}, err
		}
		if err := s.ceph.MarkOut(ctx, id); err != nil {
			// Reversible and idempotent: resent below while the OSD
			// still reads in.
			return s.wait(fmt.Sprintf("ceph osd out %d failed, retrying: %v", id, err))
		}
		s.event(fmt.Sprintf("osd.%d marked out (undo: ceph osd in %d)", id, id))
		return s.wait(fmt.Sprintf("osd.%d out; waiting for every PG active+clean", id))
	}
	osds, err := s.ceph.OSDs(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
	}
	me := findOSD(osds, id)
	if me == nil {
		return s.pause(fmt.Sprintf("osd.%d disappeared from Ceph during the drain", id))
	}
	elapsed := since(o.Status.MarkedOutAt, now)
	if me.In {
		if elapsed < osdMinMovementWait {
			// The out failed or is not visible yet: resend it.
			if err := s.ceph.MarkOut(ctx, id); err != nil {
				return s.wait(fmt.Sprintf("ceph osd out %d failed, retrying: %v", id, err))
			}
			return s.wait(fmt.Sprintf("osd.%d out; waiting for every PG active+clean", id))
		}
		return s.pause(fmt.Sprintf("osd.%d reads in %s after it was marked out (marked in again by someone else?)", id, elapsed.Truncate(time.Second)))
	}
	st, err := s.ceph.Status(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading ceph status: %v", err))
	}
	s.recordPGs(st)
	if !st.AllActiveClean() || (!o.Status.SawUnclean && elapsed < osdMinMovementWait) {
		if elapsed > s.drainTimeout() {
			return s.pause(fmt.Sprintf("PGs not all active+clean after %s (%s); nothing undone, osd.%d stays out", s.drainTimeout(), o.Status.PGs, id))
		}
		return s.wait(fmt.Sprintf("draining osd.%d: %s PGs active+clean", id, o.Status.PGs))
	}
	safe, why, err := s.ceph.SafeToDestroy(ctx, id)
	if err != nil {
		return s.wait(fmt.Sprintf("asking safe-to-destroy: %v", err))
	}
	if !safe {
		return s.pause(fmt.Sprintf("ceph osd safe-to-destroy %d refuses: %s", id, why))
	}
	s.enter(framev1beta1.OSDReplacementAwaitingPurgeApproval, fmt.Sprintf(
		"osd.%d is drained and safe to destroy. Approval no. 1: retype the old disk's serial (%s) to purge it — this cannot be undone", id, o.Status.OldDisk.Serial))
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------- approvals

// armApproval makes the step now waiting for an approval count only an
// approval given from now on: one already in spec (written in advance, or
// left from before a pause) is cleared, and the generation is recorded.
// It reports whether the step was already armed; when it was not, the
// caller returns and the next reconcile reads the approval.
func (s *osdStep) armApproval(ctx context.Context, field func(*framev1beta1.FrameOSDReplacementSpec) **framev1beta1.SerialApproval) (bool, error) {
	o := s.o
	if a := *field(&o.Spec); o.Status.ApprovalArmedGeneration > 0 && (a == nil || o.Generation > o.Status.ApprovalArmedGeneration) {
		return true, nil
	}
	if *field(&o.Spec) != nil {
		// A copy: the patch's answer must not overwrite this reconcile's
		// status in memory.
		cp := o.DeepCopy()
		base := cp.DeepCopy()
		*field(&cp.Spec) = nil
		if err := s.r.Patch(ctx, cp, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return false, err
		}
		*field(&o.Spec) = nil
		o.Generation, o.ResourceVersion = cp.Generation, cp.ResourceVersion
		// The status is unchanged by a spec patch: the stored object is
		// base with the new spec and version.
		*field(&s.base.Spec) = nil
		s.base.Generation, s.base.ResourceVersion = cp.Generation, cp.ResourceVersion
		s.event("an approval written before this step was cleared: it must be given now")
	}
	o.Status.ApprovalArmedGeneration = o.Generation
	return false, nil
}

func purgeApprovalField(sp *framev1beta1.FrameOSDReplacementSpec) **framev1beta1.SerialApproval {
	return &sp.PurgeApproval
}

func wipeApprovalField(sp *framev1beta1.FrameOSDReplacementSpec) **framev1beta1.SerialApproval {
	return &sp.WipeApproval
}

// ---------------------------------------------------------------- approval 1

func (s *osdStep) awaitPurgeApproval(ctx context.Context) (ctrl.Result, error) {
	o, id := s.o, s.o.Spec.OSDID
	if armed, err := s.armApproval(ctx, purgeApprovalField); err != nil {
		return s.wait(fmt.Sprintf("clearing an approval given before this step: %v", err))
	} else if !armed {
		return ctrl.Result{}, nil
	}
	a := o.Spec.PurgeApproval
	if a == nil {
		return ctrl.Result{}, nil
	}
	if a.Serial != o.Status.OldDisk.Serial {
		o.Status.Message = clipString(fmt.Sprintf("the serial typed (%s) is not the old disk's; nothing done. Retype %s to purge osd.%d", a.Serial, o.Status.OldDisk.Serial, id), 1024)
		return ctrl.Result{}, nil
	}
	// The state may have moved since the drain: look again.
	osds, err := s.ceph.OSDs(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
	}
	me := findOSD(osds, id)
	if me == nil || me.In || me.Host != o.Status.Host {
		return s.pause(fmt.Sprintf("osd.%d is no longer out on %s; not purging", id, o.Status.Host))
	}
	safe, why, err := s.ceph.SafeToDestroy(ctx, id)
	if err != nil {
		return s.wait(fmt.Sprintf("asking safe-to-destroy: %v", err))
	}
	if !safe {
		return s.pause(fmt.Sprintf("ceph osd safe-to-destroy %d now refuses: %s", id, why))
	}
	o.Status.PurgeApprovedAt = s.nowTime()
	s.event(fmt.Sprintf("approval no. 1 given (serial %s retyped)", a.Serial))
	s.enter(framev1beta1.OSDReplacementPurging, fmt.Sprintf("stopping osd.%d", id))
	return ctrl.Result{Requeue: true}, nil
}

// ---------------------------------------------------------------- Purging

func (s *osdStep) osdDeployment() types.NamespacedName {
	return types.NamespacedName{Namespace: s.r.rookNamespace(), Name: fmt.Sprintf("rook-ceph-osd-%d", s.o.Spec.OSDID)}
}

func (s *osdStep) scaleOSD(ctx context.Context, replicas int32) error {
	var d appsv1.Deployment
	if err := s.r.Get(ctx, s.osdDeployment(), &d); err != nil {
		return err
	}
	base := d.DeepCopy()
	d.Spec.Replicas = &replicas
	return s.r.Patch(ctx, &d, client.MergeFrom(base))
}

func (s *osdStep) purge(ctx context.Context) (ctrl.Result, error) {
	o, id, now := s.o, s.o.Spec.OSDID, s.r.now()
	if o.Status.PurgedAt == nil {
		if o.Status.PurgeRequestedAt == nil {
			if o.Status.ScaledDownAt == nil {
				o.Status.ScaledDownAt = s.nowTime()
				if err := s.save(ctx); err != nil {
					return ctrl.Result{}, err
				}
				s.event(fmt.Sprintf("deployment %s scaled to 0", s.osdDeployment().Name))
			}
			if err := s.scaleOSD(ctx, 0); err != nil && !apierrors.IsNotFound(err) {
				return s.wait(fmt.Sprintf("scaling %s to 0: %v", s.osdDeployment().Name, err))
			}
			var pods corev1.PodList
			if err := s.r.List(ctx, &pods, client.InNamespace(s.r.rookNamespace()), client.MatchingLabels{"ceph-osd-id": fmt.Sprint(id)}); err != nil {
				return s.wait(fmt.Sprintf("listing osd.%d's pods: %v", id, err))
			}
			osds, err := s.ceph.OSDs(ctx)
			if err != nil {
				return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
			}
			me := findOSD(osds, id)
			if me == nil {
				return s.pause(fmt.Sprintf("osd.%d vanished from Ceph before the purge was sent; check `ceph osd tree`", id))
			}
			if len(pods.Items) > 0 || me.Up {
				if since(o.Status.ScaledDownAt, now) > osdScaleDownTimeout {
					return s.pause(fmt.Sprintf("osd.%d still up (%d pod(s)) %s after scaling to 0", id, len(pods.Items), osdScaleDownTimeout))
				}
				return s.wait(fmt.Sprintf("waiting for osd.%d to stop", id))
			}
			if me.In {
				return s.pause(fmt.Sprintf("osd.%d is in again; not purging", id))
			}
			safe, why, err := s.ceph.SafeToDestroy(ctx, id)
			if err != nil {
				return s.wait(fmt.Sprintf("asking safe-to-destroy: %v", err))
			}
			if !safe {
				return s.pause(fmt.Sprintf("ceph osd safe-to-destroy %d refuses right before the purge: %s", id, why))
			}
			// Write-ahead: once this is recorded the purge is never sent
			// again by this object, whatever happens next.
			o.Status.PurgeRequestedAt = s.nowTime()
			if err := s.save(ctx); err != nil {
				return ctrl.Result{}, err
			}
			if err := s.ceph.Purge(ctx, id); err != nil {
				s.event(fmt.Sprintf("purge of osd.%d sent, answer: %v", id, err))
				return s.pause(fmt.Sprintf("the purge of osd.%d was sent and failed or its answer was lost (%v); it is never resent: check `ceph osd tree`, purge by hand if needed, then resume", id, err))
			}
			s.event(fmt.Sprintf("purge of osd.%d sent", id))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		osds, err := s.ceph.OSDs(ctx)
		if err != nil {
			return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
		}
		if findOSD(osds, id) != nil {
			if since(o.Status.PurgeRequestedAt, now) < osdPurgeVisibleWithin {
				return s.wait(fmt.Sprintf("waiting for osd.%d to leave the OSD map", id))
			}
			return s.pause(fmt.Sprintf("the purge of osd.%d was sent at %s but the OSD still exists; it is never resent: check `ceph osd tree`, purge by hand if needed, then resume",
				id, o.Status.PurgeRequestedAt.UTC().Format(time.RFC3339)))
		}
		o.Status.PurgedAt = s.nowTime()
		s.event(fmt.Sprintf("osd.%d purged (disk %s, %s)", id, o.Status.OldDisk.Serial, o.Status.OldDisk.ByIDPath))
	}
	var d appsv1.Deployment
	d.Name, d.Namespace = s.osdDeployment().Name, s.osdDeployment().Namespace
	if err := s.r.Delete(ctx, &d); err != nil && !apierrors.IsNotFound(err) {
		return s.wait(fmt.Sprintf("deleting deployment %s: %v", d.Name, err))
	}
	s.enter(framev1beta1.OSDReplacementAwaitingDisk, fmt.Sprintf(
		"osd.%d purged. Remove the disk with serial %s from %s, insert the new one (in Non-RAID mode on a PERC/H310). Frame waits for it to appear",
		id, o.Status.OldDisk.Serial, o.Status.Host))
	return ctrl.Result{RequeueAfter: osdReplacementPoll}, nil
}

// ---------------------------------------------------------------- AwaitingDisk

func (s *osdStep) awaitDisk(ctx context.Context) (ctrl.Result, error) {
	o := s.o
	m, err := s.machine(ctx, o.Status.Host)
	if err != nil {
		return s.orPause(err)
	}
	if !m.Status.Storage.ObservedAt.After(o.Status.PurgedAt.Time) {
		return s.wait("waiting for a disk report newer than the purge")
	}
	osds, err := s.ceph.OSDs(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
	}
	var known []string
	for _, h := range osds {
		hm, err := s.ceph.Metadata(ctx, h.ID)
		if err != nil {
			return s.wait(fmt.Sprintf("reading osd.%d's metadata: %v", h.ID, err))
		}
		known = append(known, hm.Serials...)
	}
	devices, err := s.r.deviceNames(ctx)
	if err != nil {
		return s.orPause(err)
	}
	var cand []framev1beta1.ObservedDisk
	oldThere := false
	for _, d := range m.Status.Storage.Observed {
		if d.SerialNumber == o.Status.OldDisk.Serial {
			oldThere = true
		}
		if d.ByIDPath == "" || slices.Contains(o.Status.DisksBefore, d.ByIDPath) || slices.Contains(devices, d.ByIDPath) ||
			slices.Contains(known, d.SerialNumber) || d.SerialNumber == o.Status.OldDisk.Serial {
			continue
		}
		cand = append(cand, d)
	}
	switch {
	case len(cand) == 0:
		msg := "waiting for the new disk to appear on " + o.Status.Host
		if oldThere {
			msg += fmt.Sprintf(" (the old disk, serial %s, is still there)", o.Status.OldDisk.Serial)
		}
		return s.wait(msg + ". Not seen? iDRAC → Storage → Physical Disks → convert it to Non-RAID")
	case len(cand) > 1:
		// Paths only: the new disk's serial is never shown before approval
		// no. 2, which the person reads on the label.
		var names []string
		for _, c := range cand {
			names = append(names, shownPath(c.ByIDPath))
		}
		return s.pause(fmt.Sprintf("%d disks appeared since the purge (%s); exactly one is expected", len(cand), strings.Join(names, ", ")))
	}
	d := cand[0]
	if d.Occupancy != "free" {
		return s.pause(fmt.Sprintf("the new disk %s is %s: the console only wipes a disk with no partition and no signature; clear it by hand (fiche B3.4), then resume", shownPath(d.ByIDPath), d.Occupancy))
	}
	switch d.LogicalSectorBytes {
	case 512, 4096:
	case 0:
		return s.pause(fmt.Sprintf("the new disk %s does not report its sector size (frame-agent too old?)", shownPath(d.ByIDPath)))
	default:
		return s.pause(fmt.Sprintf("the new disk %s has %d-byte sectors: it needs a low-level format (sg_format) before use", shownPath(d.ByIDPath), d.LogicalSectorBytes))
	}
	if d.SMART != nil && d.SMART.Passed != nil && !*d.SMART.Passed {
		return s.pause(fmt.Sprintf("the new disk %s fails its SMART self-assessment", shownPath(d.ByIDPath)))
	}
	o.Status.NewDisk = replaced(d)
	s.enter(framev1beta1.OSDReplacementAwaitingWipeApproval, fmt.Sprintf(
		"new disk found: %s, %s, %d GB. Approval no. 2: read the serial on its LABEL and retype it to wipe this disk — this cannot be undone",
		shownPath(d.ByIDPath), d.Model, d.SizeGB))
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------- approval 2

func (s *osdStep) awaitWipeApproval(ctx context.Context) (ctrl.Result, error) {
	o := s.o
	if armed, err := s.armApproval(ctx, wipeApprovalField); err != nil {
		return s.wait(fmt.Sprintf("clearing an approval given before this step: %v", err))
	} else if !armed {
		return ctrl.Result{}, nil
	}
	a := o.Spec.WipeApproval
	if a == nil {
		return ctrl.Result{}, nil
	}
	if a.Serial != o.Status.NewDisk.Serial {
		o.Status.Message = clipString(fmt.Sprintf("the serial typed (%s) is not the new disk's; nothing done. Retype the serial on the label of %s", a.Serial, shownPath(o.Status.NewDisk.ByIDPath)), 1024)
		return ctrl.Result{}, nil
	}
	o.Status.WipeApprovedAt = s.nowTime()
	s.event(fmt.Sprintf("approval no. 2 given (serial %s retyped)", a.Serial))
	s.enter(framev1beta1.OSDReplacementWiping, fmt.Sprintf("asking the agent on %s to wipe %s", o.Status.Host, o.Status.NewDisk.ByIDPath))
	return ctrl.Result{Requeue: true}, nil
}

// ---------------------------------------------------------------- Wiping

func (s *osdStep) wipe(ctx context.Context) (ctrl.Result, error) {
	o, now := s.o, s.r.now()
	if o.Status.WipeRequestedAt == nil {
		o.Status.WipeRequestID = fmt.Sprintf("%s-%d", strings.ToLower(string(o.UID)), o.Status.ObservedResume)
		o.Status.WipeRequestedAt = s.nowTime()
		if err := s.save(ctx); err != nil {
			return ctrl.Result{}, err
		}
		s.event(fmt.Sprintf("wipe of %s requested (%s)", o.Status.NewDisk.ByIDPath, o.Status.WipeRequestID))
	}
	var node corev1.Node
	if err := s.r.Get(ctx, types.NamespacedName{Name: o.Status.Host}, &node); err != nil {
		return s.wait(fmt.Sprintf("reading node %s: %v", o.Status.Host, err))
	}
	if raw := node.Annotations[framev1beta1.DiskWipeResultAnnotation]; raw != "" {
		var res framev1beta1.DiskWipeResult
		if json.Unmarshal([]byte(raw), &res) == nil && res.ID == o.Status.WipeRequestID {
			if !res.OK {
				_ = s.clearWipeRequest(ctx)
				return s.pause(fmt.Sprintf("the agent did not wipe the disk (%s): %s", map[bool]string{true: "a command had started", false: "nothing was touched"}[res.Ran], res.Message))
			}
			base := node.DeepCopy()
			delete(node.Annotations, framev1beta1.DiskWipeRequestAnnotation)
			if err := s.r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
				return s.wait(fmt.Sprintf("clearing the wipe request on %s: %v", node.Name, err))
			}
			o.Status.WipedAt = s.nowTime()
			s.event("agent: " + res.Message)
			s.enter(framev1beta1.OSDReplacementDeclaring, fmt.Sprintf("declaring %s to Rook in place of %s", o.Status.NewDisk.ByIDPath, o.Status.OldDisk.ByIDPath))
			return ctrl.Result{Requeue: true}, nil
		}
	}
	want := framev1beta1.DiskWipeRequest{ID: o.Status.WipeRequestID, ByIDPath: o.Status.NewDisk.ByIDPath, Serial: o.Status.NewDisk.Serial, RequestedAt: *o.Status.WipeRequestedAt}
	var cur framev1beta1.DiskWipeRequest
	if raw := node.Annotations[framev1beta1.DiskWipeRequestAnnotation]; raw == "" || json.Unmarshal([]byte(raw), &cur) != nil || cur.ID != want.ID {
		// Writing the request is not the wipe: the agent acts on an id
		// once, after its own checks. Rewriting it is safe.
		out, _ := json.Marshal(want)
		base := node.DeepCopy()
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		node.Annotations[framev1beta1.DiskWipeRequestAnnotation] = string(out)
		if err := s.r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
			return s.wait(fmt.Sprintf("writing the wipe request on %s: %v", node.Name, err))
		}
	}
	if since(o.Status.WipeRequestedAt, now) > osdWipeAnswerTimeout {
		if err := s.clearWipeRequest(ctx); err != nil {
			return s.wait(fmt.Sprintf("no answer from frame-agent; withdrawing the wipe request on %s: %v", o.Status.Host, err))
		}
		return s.pause(fmt.Sprintf("no answer from frame-agent on %s after %s (is it running?); the request is withdrawn. If the agent was mid-wipe its answer still comes: outcome unknown, inspect the disk before resuming", o.Status.Host, osdWipeAnswerTimeout))
	}
	return s.wait(fmt.Sprintf("waiting for frame-agent on %s to wipe %s", o.Status.Host, o.Status.NewDisk.ByIDPath))
}

// wipeState says what is known of the wipe, for a cancel: the agent's
// answer is read by request id, since it may have come after the request
// was withdrawn.
func (s *osdStep) wipeState(ctx context.Context) string {
	o := s.o
	switch {
	case o.Status.WipedAt != nil:
		return "wiped"
	case o.Status.WipeRequestedAt == nil:
		return "not wiped"
	}
	var node corev1.Node
	if err := s.r.Get(ctx, types.NamespacedName{Name: o.Status.Host}, &node); err == nil {
		var res framev1beta1.DiskWipeResult
		if raw := node.Annotations[framev1beta1.DiskWipeResultAnnotation]; raw != "" && json.Unmarshal([]byte(raw), &res) == nil && res.ID == o.Status.WipeRequestID {
			switch {
			case res.OK:
				return "wiped (the agent's answer came after the request)"
			case !res.Ran:
				return "not wiped (the agent refused: " + clipString(res.Message, 200) + ")"
			default:
				return "wipe outcome unknown (a command had started) — inspect the disk"
			}
		}
	}
	return fmt.Sprintf("wipe requested at %s with no answer: outcome unknown — inspect the disk", o.Status.WipeRequestedAt.UTC().Format(time.RFC3339))
}

// clearWipeRequest withdraws this replacement's wipe request from the
// Node, so that the agent cannot act on it after a pause or a cancel.
func (s *osdStep) clearWipeRequest(ctx context.Context) error {
	o := s.o
	if o.Status.Host == "" || o.Status.WipeRequestID == "" {
		return nil
	}
	var node corev1.Node
	if err := s.r.Get(ctx, types.NamespacedName{Name: o.Status.Host}, &node); err != nil {
		return client.IgnoreNotFound(err)
	}
	var cur framev1beta1.DiskWipeRequest
	raw := node.Annotations[framev1beta1.DiskWipeRequestAnnotation]
	if raw == "" || (json.Unmarshal([]byte(raw), &cur) == nil && cur.ID != o.Status.WipeRequestID) {
		return nil
	}
	base := node.DeepCopy()
	delete(node.Annotations, framev1beta1.DiskWipeRequestAnnotation)
	return s.r.Patch(ctx, &node, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// ---------------------------------------------------------------- Declaring

func (s *osdStep) declare(ctx context.Context) (ctrl.Result, error) {
	o := s.o
	oldName, newName := o.Status.OldDisk.ByIDPath, o.Status.NewDisk.ByIDPath
	devices, err := s.r.deviceNames(ctx)
	if err != nil {
		return s.orPause(err)
	}
	if !slices.Contains(devices, newName) || slices.Contains(devices, oldName) {
		if o.Status.DeclareRequestedAt == nil {
			o.Status.DeclareRequestedAt = s.nowTime()
		}
		i := *o.Status.DeviceIndex
		patch, _ := json.Marshal([]map[string]any{
			{"op": "test", "path": fmt.Sprintf("/spec/storage/devices/%d/name", i), "value": oldName},
			{"op": "replace", "path": fmt.Sprintf("/spec/storage/devices/%d/name", i), "value": newName},
		})
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(cephClusterGVK)
		u.SetNamespace(s.r.rookNamespace())
		u.SetName(s.r.clusterName())
		if err := s.r.Patch(ctx, u, client.RawPatch(types.JSONPatchType, patch)); err != nil {
			if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || strings.Contains(err.Error(), "test") {
				return s.pause(fmt.Sprintf("the CephCluster refused the patch of entry %d (%s → %s): %v. Has its device list changed?", i, oldName, newName, err))
			}
			return s.wait(fmt.Sprintf("patching the CephCluster: %v", err))
		}
		devices, err = s.r.deviceNames(ctx)
		if err != nil {
			return s.orPause(err)
		}
		if !slices.Contains(devices, newName) || slices.Contains(devices, oldName) {
			return s.pause(fmt.Sprintf("the CephCluster did not take the new device list: %v", devices))
		}
	}
	o.Status.DeclaredAt = s.nowTime()
	s.event(fmt.Sprintf("CephCluster entry %d: %s → %s. Update k8s/storage/cephcluster.yaml in the Neura repo to match", *o.Status.DeviceIndex, oldName, newName))
	s.enter(framev1beta1.OSDReplacementAwaitingOSD, fmt.Sprintf("waiting for Rook to create the new OSD on %s", o.Status.Host))
	return ctrl.Result{RequeueAfter: osdReplacementPoll}, nil
}

// ---------------------------------------------------------------- AwaitingOSD

func (s *osdStep) awaitOSD(ctx context.Context) (ctrl.Result, error) {
	o, now := s.o, s.r.now()
	osds, err := s.ceph.OSDs(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
	}
	host := hostOSDs(osds, o.Status.Host)
	up := 0
	for _, h := range host {
		if h.Up && h.In {
			up++
		}
	}
	if int32(up) >= o.Status.HostOSDsBefore {
		var ids []string
		for _, h := range host {
			ids = append(ids, fmt.Sprintf("osd.%d", h.ID))
		}
		s.event(fmt.Sprintf("%s has %d OSDs up and in again: %s", o.Status.Host, up, strings.Join(ids, ", ")))
		o.Status.BackfillSince = s.nowTime()
		o.Status.SawUnclean = false
		s.enter(framev1beta1.OSDReplacementBackfilling, "waiting for every PG active+clean")
		return ctrl.Result{RequeueAfter: osdReplacementPoll}, nil
	}
	elapsed := since(o.Status.DeclaredAt, now)
	if elapsed > osdOperatorRestartAfter && o.Status.OperatorRestartedAt == nil {
		var d appsv1.Deployment
		if err := s.r.Get(ctx, types.NamespacedName{Namespace: s.r.rookNamespace(), Name: "rook-ceph-operator"}, &d); err != nil {
			return s.wait(fmt.Sprintf("reading rook-ceph-operator: %v", err))
		}
		base := d.DeepCopy()
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = map[string]string{}
		}
		d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = now.UTC().Format(time.RFC3339)
		if err := s.r.Patch(ctx, &d, client.MergeFrom(base)); err != nil {
			return s.wait(fmt.Sprintf("restarting rook-ceph-operator: %v", err))
		}
		o.Status.OperatorRestartedAt = s.nowTime()
		s.event("no new OSD after 10 min: rook-ceph-operator restarted (once)")
	}
	if elapsed > osdNewOSDTimeout {
		return s.pause(fmt.Sprintf("%s has %d of %d OSDs up after %s; the CephCluster is up to date — look at the osd-prepare job's log", o.Status.Host, up, o.Status.HostOSDsBefore, osdNewOSDTimeout))
	}
	return s.wait(fmt.Sprintf("waiting for the new OSD: %s has %d of %d OSDs up and in", o.Status.Host, up, o.Status.HostOSDsBefore))
}

// ---------------------------------------------------------------- Backfilling

func (s *osdStep) backfill(ctx context.Context) (ctrl.Result, error) {
	o, now := s.o, s.r.now()
	st, err := s.ceph.Status(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading ceph status: %v", err))
	}
	s.recordPGs(st)
	elapsed := since(o.Status.BackfillSince, now)
	if !st.AllActiveClean() || (!o.Status.SawUnclean && elapsed < osdMinMovementWait) {
		if elapsed > s.drainTimeout() {
			return s.pause(fmt.Sprintf("PGs not all active+clean after %s (%s)", s.drainTimeout(), o.Status.PGs))
		}
		return s.wait(fmt.Sprintf("backfilling: %s PGs active+clean", o.Status.PGs))
	}
	s.enter(framev1beta1.OSDReplacementSucceeded, fmt.Sprintf(
		"osd.%d replaced: %s (serial %s) took the place of %s (serial %s); every PG active+clean. Update k8s/storage/cephcluster.yaml in the Neura repo, and keep the old disk labelled for a week",
		o.Spec.OSDID, o.Status.NewDisk.ByIDPath, o.Status.NewDisk.Serial, o.Status.OldDisk.ByIDPath, o.Status.OldDisk.Serial))
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------- cancel

func (s *osdStep) cancel(ctx context.Context) (ctrl.Result, error) {
	o, id := s.o, s.o.Spec.OSDID
	if o.Status.PurgeRequestedAt != nil {
		if o.Status.WipedAt == nil {
			if err := s.clearWipeRequest(ctx); err != nil {
				return s.wait(fmt.Sprintf("cancelling: withdrawing the wipe request on %s: %v", o.Status.Host, err))
			}
		}
		s.enter(framev1beta1.OSDReplacementCancelled, fmt.Sprintf(
			"cancelled after the purge of osd.%d; nothing undone. Where things stand: purged %t, new disk %s, %s, declared %t. Finish by hand with the fiche (B3–B5)",
			id, o.Status.PurgedAt != nil, diskName(o.Status.NewDisk, o.Status.WipeApprovedAt != nil), s.wipeState(ctx), o.Status.DeclaredAt != nil))
		return ctrl.Result{}, nil
	}
	if o.Status.ScaledDownAt != nil {
		if err := s.scaleOSD(ctx, 1); err != nil && !apierrors.IsNotFound(err) {
			return s.wait(fmt.Sprintf("cancelling: scaling %s back to 1: %v", s.osdDeployment().Name, err))
		}
	}
	if o.Status.MarkedOutAt != nil {
		op, err := s.r.Ceph(ctx)
		if err != nil {
			return s.wait(fmt.Sprintf("cancelling: the Ceph operator account is not usable: %v", err))
		}
		osds, err := op.OSDs(ctx)
		if err != nil {
			return s.wait(fmt.Sprintf("cancelling: reading the OSD list: %v", err))
		}
		if findOSD(osds, id) == nil {
			s.enter(framev1beta1.OSDReplacementCancelled, fmt.Sprintf("cancelled: osd.%d no longer exists in Ceph (removed by hand?); nothing to mark in. Check `ceph osd tree`", id))
			return ctrl.Result{}, nil
		}
		if err := op.MarkIn(ctx, id); err != nil {
			return s.wait(fmt.Sprintf("cancelling: ceph osd in %d failed, retrying: %v", id, err))
		}
		s.enter(framev1beta1.OSDReplacementCancelled, fmt.Sprintf("cancelled: osd.%d marked in again; Ceph moves its data back", id))
		return ctrl.Result{}, nil
	}
	s.enter(framev1beta1.OSDReplacementCancelled, "cancelled before anything was changed")
	return ctrl.Result{}, nil
}

// diskName names the new disk without its serial, which is shown only
// once approval no. 2 has been given.
func diskName(d *framev1beta1.ReplacedDisk, approved bool) string {
	if d == nil {
		return "no disk"
	}
	if approved {
		return d.ByIDPath
	}
	return shownPath(d.ByIDPath)
}

// shownPath is a by-id path as shown before approval no. 2. An NVMe path
// of the nvme-nvme.<vendor>-<hex serial>-<hex model>-<ns> form carries the
// serial (in hex): only its prefix is shown. wwn- and nvme-eui. paths do
// not, and are shown whole.
func shownPath(p string) string {
	const nvme = "/dev/disk/by-id/nvme-nvme."
	if strings.HasPrefix(p, nvme) {
		rest := p[len(nvme):]
		if i := strings.IndexByte(rest, '-'); i >= 0 {
			rest = rest[:i]
		}
		return nvme + rest + "-…"
	}
	return p
}

func clipString(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (r *FrameOSDReplacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameOSDReplacement{}).
		Named("frameosdreplacement").
		Complete(r)
}
