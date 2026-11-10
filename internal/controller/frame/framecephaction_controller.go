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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/cephmgr"
)

// CephActionOperator is what FrameCephAction needs from the Ceph operator
// account (cephmgr.Operator).
type CephActionOperator interface {
	Status(ctx context.Context) (*cephmgr.ClusterStatus, error)
	OSDs(ctx context.Context) ([]cephmgr.OSDState, error)
	PoolSizes(ctx context.Context) ([]cephmgr.Pool, error)
	MarkOut(ctx context.Context, id int32) error
	MarkIn(ctx context.Context, id int32) error
	Scrub(ctx context.Context, id int32, deep bool) error
	Flags(ctx context.Context) ([]string, error)
	SetFlag(ctx context.Context, flag string, on bool) (bool, error)
}

const (
	cephActionPoll           = 30 * time.Second
	cephActionRestartTimeout = 15 * time.Minute
	cephActionDefaultMinutes = 120
	// cephActionSettle is how long an out or a restart that ended Done
	// still holds back the next out or restart: peering and recovery after
	// it may not show in the PG states yet.
	cephActionSettle = 10 * time.Minute
	// cephActionFinalizer keeps a SetFlag's object until the flag it set is
	// unset: deleting the object must not leave the flag set for good.
	cephActionFinalizer = "frame.plume-labs.io/ceph-action-flag"
)

// stopBlockingFlags are the cluster flags under which an out or a restart
// is refused: with them set Ceph does not bring the OSD back, or does not
// move the data it held, so the cluster stays degraded.
var stopBlockingFlags = []string{"noup", "norecover", "nobackfill", "norebalance", "pause", "pauserd", "pausewr"}

// FrameCephActionReconciler sends one everyday Ceph gesture per object.
//
// Its rules:
//   - a gesture is sent at most once: status.sentAt is written (with the
//     object's resourceVersion) before it is sent, and an object with
//     sentAt never sends it again;
//   - out and restart stop an OSD's share of the data: they are sent only
//     when every PG is active+clean, every OSD up and in and every pool
//     keeps min_size with one copy fewer (cephmgr.OKToStopVerdict, the
//     stand-in for `ceph osd ok-to-stop`), so never while another host is
//     degraded, nor while another out or restart is in flight or ended less
//     than cephActionSettle ago, nor under a flag that keeps the data from
//     moving (stopBlockingFlags);
//   - while an OSD replacement is running, only scrubs are sent;
//   - a flag is set for a while and unset at its end or on release, unless
//     it was set before (by hand); a second object for a flag already held
//     is refused, so one object owns each flag; a finalizer unsets it if
//     the object is deleted while it holds it.
type FrameCephActionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Ceph   func(ctx context.Context) (CephActionOperator, error)
	// APIReader reads other actions past the cache, so that two SetFlags
	// for one flag cannot both see it free. Nil: the client.
	APIReader client.Reader
	// RookNamespace holds the rook-ceph-osd-N deployments.
	RookNamespace string
	Now           func() time.Time
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framecephactions,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framecephactions/finalizers,verbs=update
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framecephactions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;patch

func (r *FrameCephActionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *FrameCephActionReconciler) rookNamespace() string {
	if r.RookNamespace != "" {
		return r.RookNamespace
	}
	return defaultRookName
}

func cephActionTerminal(phase string) bool {
	return phase == framev1beta1.CephActionDone || phase == framev1beta1.CephActionRefused || phase == framev1beta1.CephActionFailed
}

type cephActionStep struct {
	r    *FrameCephActionReconciler
	a    *framev1beta1.FrameCephAction
	ceph CephActionOperator
	// base is the object as last stored: every status patch is computed
	// from it, carries its resourceVersion, then base moves forward.
	base *framev1beta1.FrameCephAction
	// sent: this reconcile wrote sentAt, so its outcome must be recorded
	// even if the object changed meanwhile.
	sent bool
}

func (r *FrameCephActionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var a framev1beta1.FrameCephAction
	if err := r.Get(ctx, req.NamespacedName, &a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !a.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &a)
	}
	if cephActionTerminal(a.Status.Phase) {
		return ctrl.Result{}, r.dropFinalizer(ctx, &a)
	}
	if a.Spec.Action == framev1beta1.CephActionSetFlag && a.Status.SentAt == nil && !controllerutil.ContainsFinalizer(&a, cephActionFinalizer) {
		base := a.DeepCopy()
		controllerutil.AddFinalizer(&a, cephActionFinalizer)
		if err := r.Patch(ctx, &a, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, err
		}
	}
	s := &cephActionStep{r: r, a: &a, base: a.DeepCopy()}
	res, err := s.run(ctx)
	if !cephActionStatusEqual(s.base, &a) {
		perr := r.Status().Patch(ctx, &a, client.MergeFromWithOptions(s.base, client.MergeFromWithOptimisticLock{}))
		if perr != nil && s.sent && apierrors.IsConflict(perr) {
			// The gesture went out in this reconcile: its outcome is
			// recorded whatever changed meanwhile (a release, a label).
			perr = r.Status().Patch(ctx, &a, client.MergeFrom(s.base))
		}
		if perr != nil {
			return ctrl.Result{}, errors.Join(err, perr)
		}
	}
	if cephActionTerminal(a.Status.Phase) {
		if ferr := r.dropFinalizer(ctx, &a); ferr != nil {
			return ctrl.Result{}, errors.Join(err, ferr)
		}
	}
	return res, err
}

// finalize runs when the object is deleted: a flag this object set and
// still holds is unset first (retried until Ceph accepts it).
func (r *FrameCephActionReconciler) finalize(ctx context.Context, a *framev1beta1.FrameCephAction) error {
	if !controllerutil.ContainsFinalizer(a, cephActionFinalizer) {
		return nil
	}
	if a.Spec.Action == framev1beta1.CephActionSetFlag && a.Status.SentAt != nil && !a.Status.FlagWasSet && a.Status.Phase == framev1beta1.CephActionActive {
		op, err := r.Ceph(ctx)
		if err != nil {
			return fmt.Errorf("%s deleted while holding %s: the Ceph operator account is not usable: %w", a.Name, a.Spec.Flag, err)
		}
		if _, err := op.SetFlag(ctx, a.Spec.Flag, false); err != nil && !isFlagDrift(err) {
			return fmt.Errorf("%s deleted while holding %s: unsetting it: %w", a.Name, a.Spec.Flag, err)
		}
	}
	return r.dropFinalizer(ctx, a)
}

func (r *FrameCephActionReconciler) dropFinalizer(ctx context.Context, a *framev1beta1.FrameCephAction) error {
	if !controllerutil.ContainsFinalizer(a, cephActionFinalizer) {
		return nil
	}
	base := a.DeepCopy()
	controllerutil.RemoveFinalizer(a, cephActionFinalizer)
	return client.IgnoreNotFound(r.Patch(ctx, a, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
}

func isFlagDrift(err error) bool {
	var d *cephmgr.FlagDriftError
	return errors.As(err, &d)
}

func (r *FrameCephActionReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func cephActionStatusEqual(a, b *framev1beta1.FrameCephAction) bool {
	ja, _ := json.Marshal(a.Status)
	jb, _ := json.Marshal(b.Status)
	return string(ja) == string(jb)
}

func (s *cephActionStep) run(ctx context.Context) (ctrl.Result, error) {
	a := s.a
	if a.Status.Phase == "" {
		a.Status.Phase = framev1beta1.CephActionPending
	}
	op, err := s.r.Ceph(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("the Ceph operator account is not usable: %v", err))
	}
	s.ceph = op
	if a.Status.SentAt == nil && (a.Spec.Action != framev1beta1.CephActionSetFlag || !a.Spec.Release) {
		why, err := s.othersRefusal(ctx)
		if err != nil {
			return s.wait(err.Error())
		}
		if why != "" {
			return s.end(framev1beta1.CephActionRefused, why+"; nothing sent")
		}
	}
	switch a.Spec.Action {
	case framev1beta1.CephActionOSDIn, framev1beta1.CephActionOSDScrub, framev1beta1.CephActionOSDDeepScrub, framev1beta1.CephActionOSDOut:
		return s.oneShot(ctx)
	case framev1beta1.CephActionOSDRestart:
		return s.restart(ctx)
	case framev1beta1.CephActionSetFlag:
		return s.flag(ctx)
	}
	return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("unknown action %q", a.Spec.Action))
}

// ---------------------------------------------------------------- helpers

func (s *cephActionStep) wait(msg string) (ctrl.Result, error) {
	s.a.Status.Message = clipString(msg, 1024)
	return ctrl.Result{RequeueAfter: cephActionPoll}, nil
}

func (s *cephActionStep) end(phase, msg string) (ctrl.Result, error) {
	s.a.Status.Phase = phase
	s.a.Status.Message = clipString(msg, 1024)
	if phase == framev1beta1.CephActionDone {
		s.a.Status.DoneAt = s.nowTime()
	}
	return ctrl.Result{}, nil
}

func (s *cephActionStep) nowTime() *metav1.Time {
	t := metav1.NewTime(s.r.now())
	return &t
}

// markSent is the write-ahead record: after it, the gesture is never sent
// again by this object. It carries every change made in memory so far and
// the stored resourceVersion, which guards against a stale copy; on
// failure the in-memory status goes back to what is stored.
func (s *cephActionStep) markSent(ctx context.Context, also func(*framev1beta1.FrameCephActionStatus)) error {
	s.a.Status.SentAt = s.nowTime()
	if also != nil {
		also(&s.a.Status)
	}
	if err := s.r.Status().Patch(ctx, s.a, client.MergeFromWithOptions(s.base, client.MergeFromWithOptimisticLock{})); err != nil {
		s.base.Status.DeepCopyInto(&s.a.Status)
		return err
	}
	s.base = s.a.DeepCopy()
	s.sent = true
	return nil
}

// othersRefusal says why this gesture must not go out because of other
// objects: a running OSD replacement (every gesture but a scrub), or, for
// an out or a restart, another out or restart still in flight or ended
// less than cephActionSettle ago.
func (s *cephActionStep) othersRefusal(ctx context.Context) (string, error) {
	a := s.a
	if a.Spec.Action != framev1beta1.CephActionOSDScrub && a.Spec.Action != framev1beta1.CephActionOSDDeepScrub {
		var reps framev1beta1.FrameOSDReplacementList
		if err := s.r.reader().List(ctx, &reps); err != nil {
			return "", fmt.Errorf("listing OSD replacements: %w", err)
		}
		for i := range reps.Items {
			if p := &reps.Items[i]; !osdTerminal(p.Status.Phase) {
				return fmt.Sprintf("the OSD replacement %s/%s is running (%s): only scrubs are sent meanwhile", p.Namespace, p.Name, orUnknown(p.Status.Phase)), nil
			}
		}
	}
	if a.Spec.Action != framev1beta1.CephActionOSDOut && a.Spec.Action != framev1beta1.CephActionOSDRestart {
		return "", nil
	}
	var all framev1beta1.FrameCephActionList
	if err := s.r.reader().List(ctx, &all); err != nil {
		return "", fmt.Errorf("listing actions: %w", err)
	}
	now := s.r.now()
	for i := range all.Items {
		o := &all.Items[i]
		if o.UID == a.UID || (o.Spec.Action != framev1beta1.CephActionOSDOut && o.Spec.Action != framev1beta1.CephActionOSDRestart) || o.Status.SentAt == nil {
			continue
		}
		switch {
		case !cephActionTerminal(o.Status.Phase):
			return fmt.Sprintf("%s/%s (%s) is still in progress", o.Namespace, o.Name, o.Spec.Action), nil
		case settleFrom(o) != nil && since(settleFrom(o), now) < cephActionSettle:
			return fmt.Sprintf("%s/%s (%s) ended less than %s ago: let the cluster settle", o.Namespace, o.Name, o.Spec.Action, cephActionSettle), nil
		}
	}
	return "", nil
}

// settleFrom is when a finished out or restart stopped being in flight:
// its doneAt, or for one that failed after it was sent, its sentAt (it may
// have acted all the same). Nil for one never sent or refused.
func settleFrom(o *framev1beta1.FrameCephAction) *metav1.Time {
	switch {
	case o.Status.Phase == framev1beta1.CephActionDone && o.Status.DoneAt != nil:
		return o.Status.DoneAt
	case o.Status.Phase == framev1beta1.CephActionFailed && o.Status.SentAt != nil:
		return o.Status.SentAt
	}
	return nil
}

// cephGestureInFlight names a sent OSD gesture (in, out, restart) whose
// outcome is not known yet, or "": the OSD replacement waits for it.
func cephGestureInFlight(items []framev1beta1.FrameCephAction) string {
	for i := range items {
		o := &items[i]
		switch o.Spec.Action {
		case framev1beta1.CephActionOSDIn, framev1beta1.CephActionOSDOut, framev1beta1.CephActionOSDRestart:
			if o.Status.SentAt != nil && !cephActionTerminal(o.Status.Phase) {
				return fmt.Sprintf("%s/%s (%s osd.%d)", o.Namespace, o.Name, o.Spec.Action, ptrOr(o.Spec.OSDID))
			}
		}
	}
	return ""
}

func ptrOr(p *int32) int32 {
	if p == nil {
		return -1
	}
	return *p
}

func orUnknown(s string) string {
	if s == "" {
		return "not started"
	}
	return s
}

// stopVerdict says whether one OSD may stop holding its data now.
func (s *cephActionStep) stopVerdict(ctx context.Context, osds []cephmgr.OSDState) (string, error) {
	flags, err := s.ceph.Flags(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the OSD flags: %w", err)
	}
	for _, f := range flags {
		if slices.Contains(stopBlockingFlags, f) {
			return fmt.Sprintf("the %s flag is set (Ceph would not recover the data)", f), nil
		}
	}
	st, err := s.ceph.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("reading ceph status: %w", err)
	}
	pools, err := s.ceph.PoolSizes(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the pools: %w", err)
	}
	if ok, why := cephmgr.OKToStopVerdict(st, osds, pools); !ok {
		return why, nil
	}
	return "", nil
}

// ---------------------------------------------------------------- in, out, scrub

func (s *cephActionStep) oneShot(ctx context.Context) (ctrl.Result, error) {
	a := s.a
	id := *a.Spec.OSDID
	if a.Status.SentAt != nil {
		return s.end(framev1beta1.CephActionFailed, fmt.Sprintf("%s osd.%d was sent at %s and its outcome was not recorded; it is never resent: check `ceph osd tree`",
			a.Spec.Action, id, a.Status.SentAt.UTC().Format(time.RFC3339)))
	}
	osds, err := s.ceph.OSDs(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
	}
	me := findOSD(osds, id)
	if me == nil {
		return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("osd.%d does not exist", id))
	}
	var send func() error
	var done string
	switch a.Spec.Action {
	case framev1beta1.CephActionOSDIn:
		if me.In {
			return s.end(framev1beta1.CephActionDone, fmt.Sprintf("osd.%d was already in; nothing sent", id))
		}
		send, done = func() error { return s.ceph.MarkIn(ctx, id) }, fmt.Sprintf("osd.%d marked in; Ceph moves data back to it", id)
	case framev1beta1.CephActionOSDOut:
		if !me.In {
			return s.end(framev1beta1.CephActionDone, fmt.Sprintf("osd.%d was already out; nothing sent", id))
		}
		why, err := s.stopVerdict(ctx, osds)
		if err != nil {
			return s.wait(err.Error())
		}
		if why != "" {
			return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("osd.%d is not ok to stop: %s; nothing sent", id, why))
		}
		send, done = func() error { return s.ceph.MarkOut(ctx, id) }, fmt.Sprintf("osd.%d marked out; Ceph moves its data to the other OSDs (mark it in to undo)", id)
	default:
		deep := a.Spec.Action == framev1beta1.CephActionOSDDeepScrub
		if !me.Up {
			return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("osd.%d is down: nothing to scrub", id))
		}
		kind := map[bool]string{true: "deep-scrub", false: "scrub"}[deep]
		send, done = func() error { return s.ceph.Scrub(ctx, id, deep) }, fmt.Sprintf("%s of osd.%d asked; Ceph runs it when its scrub schedule allows", kind, id)
	}
	if err := s.markSent(ctx, nil); err != nil {
		return ctrl.Result{}, err
	}
	if err := send(); err != nil {
		return s.end(framev1beta1.CephActionFailed, fmt.Sprintf("%s osd.%d was sent and failed or its answer was lost (%v); it is never resent: check `ceph osd tree`", a.Spec.Action, id, err))
	}
	return s.end(framev1beta1.CephActionDone, done)
}

// ---------------------------------------------------------------- restart

func (s *cephActionStep) restart(ctx context.Context) (ctrl.Result, error) {
	a, now := s.a, s.r.now()
	id := *a.Spec.OSDID
	key := types.NamespacedName{Namespace: s.r.rookNamespace(), Name: fmt.Sprintf("rook-ceph-osd-%d", id)}
	var d appsv1.Deployment
	if err := s.r.Get(ctx, key, &d); err != nil {
		if apierrors.IsNotFound(err) {
			if a.Status.SentAt != nil {
				return s.end(framev1beta1.CephActionFailed, fmt.Sprintf("%s disappeared after the restart was sent: check osd.%d with `ceph osd tree`", key.Name, id))
			}
			return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("no deployment %s", key.Name))
		}
		return s.wait(fmt.Sprintf("reading %s: %v", key.Name, err))
	}
	osds, err := s.ceph.OSDs(ctx)
	if err != nil {
		return s.wait(fmt.Sprintf("reading the OSD list: %v", err))
	}
	me := findOSD(osds, id)
	if a.Status.SentAt == nil {
		if me == nil {
			return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("osd.%d does not exist", id))
		}
		why, err := s.stopVerdict(ctx, osds)
		if err != nil {
			return s.wait(err.Error())
		}
		if why != "" {
			return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("osd.%d is not ok to stop: %s; nothing done", id, why))
		}
		if err := s.markSent(ctx, nil); err != nil {
			return ctrl.Result{}, err
		}
		// The rollout restart kubectl does: a new pod template, so the
		// deployment replaces its one pod. Rook's own settings are kept.
		base := d.DeepCopy()
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = map[string]string{}
		}
		d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = a.Status.SentAt.UTC().Format(time.RFC3339)
		if err := s.r.Patch(ctx, &d, client.MergeFrom(base)); err != nil {
			return s.end(framev1beta1.CephActionFailed, fmt.Sprintf("restarting %s failed (%v); not retried", key.Name, err))
		}
		return s.wait(fmt.Sprintf("osd.%d restarting", id))
	}
	if d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] != a.Status.SentAt.UTC().Format(time.RFC3339) {
		return s.end(framev1beta1.CephActionFailed, fmt.Sprintf("the restart of osd.%d was recorded but %s no longer carries its mark: Rook rewrote the deployment or the restart was interrupted; check the pod's age before creating a new restart (not retried)", id, key.Name))
	}
	rolled := d.Status.ObservedGeneration >= d.Generation && d.Spec.Replicas != nil &&
		d.Status.UpdatedReplicas == *d.Spec.Replicas && d.Status.AvailableReplicas == *d.Spec.Replicas
	if rolled && me != nil && me.Up {
		return s.end(framev1beta1.CephActionDone, fmt.Sprintf("osd.%d restarted and up again", id))
	}
	if since(a.Status.SentAt, now) > cephActionRestartTimeout {
		return s.end(framev1beta1.CephActionFailed, fmt.Sprintf("osd.%d not up again %s after its restart: look at the pod %s-…", id, cephActionRestartTimeout, key.Name))
	}
	return s.wait(fmt.Sprintf("osd.%d restarting", id))
}

// ---------------------------------------------------------------- flags

func (s *cephActionStep) flag(ctx context.Context) (ctrl.Result, error) {
	a, now := s.a, s.r.now()
	flag := a.Spec.Flag
	if a.Status.SentAt == nil {
		var all framev1beta1.FrameCephActionList
		if err := s.r.reader().List(ctx, &all); err != nil {
			return s.wait(fmt.Sprintf("listing actions: %v", err))
		}
		for i := range all.Items {
			o := &all.Items[i]
			if o.UID != a.UID && o.Spec.Action == framev1beta1.CephActionSetFlag && o.Spec.Flag == flag && o.Status.SentAt != nil && !cephActionTerminal(o.Status.Phase) {
				return s.end(framev1beta1.CephActionRefused, fmt.Sprintf("%s is already held by %s/%s until %s; release it first", flag, o.Namespace, o.Name, timeOrUnknown(o.Status.ExpiresAt)))
			}
		}
		if a.Spec.Release {
			return s.end(framev1beta1.CephActionDone, "released before it was set; nothing sent")
		}
		cur, err := s.ceph.Flags(ctx)
		if err != nil {
			return s.wait(fmt.Sprintf("reading the OSD flags: %v", err))
		}
		was := false
		for _, f := range cur {
			if f == flag {
				was = true
			}
		}
		minutes := a.Spec.DurationMinutes
		if minutes <= 0 {
			minutes = cephActionDefaultMinutes
		}
		exp := metav1.NewTime(now.Add(time.Duration(minutes) * time.Minute))
		if err := s.markSent(ctx, func(st *framev1beta1.FrameCephActionStatus) {
			st.FlagWasSet, st.ExpiresAt, st.Phase = was, &exp, framev1beta1.CephActionActive
		}); err != nil {
			return ctrl.Result{}, err
		}
		if was {
			a.Status.Message = fmt.Sprintf("%s was already set; it stays set until %s, and is left set then (it was set before)", flag, timeOrUnknown(a.Status.ExpiresAt))
			return ctrl.Result{RequeueAfter: time.Until(exp.Time)}, nil
		}
		was, err = s.ceph.SetFlag(ctx, flag, true)
		if was {
			// Set by someone else between the two reads: not ours to unset.
			a.Status.FlagWasSet = true
			a.Status.Message = fmt.Sprintf("%s was set by someone else just before; it stays set until %s, and is left set then", flag, timeOrUnknown(a.Status.ExpiresAt))
			return ctrl.Result{RequeueAfter: cephActionPoll}, nil
		}
		if err != nil && !isFlagDrift(err) {
			// Outcome unknown: the end of the duration still unsets it.
			a.Status.Message = clipString(fmt.Sprintf("setting %s failed or its answer was lost (%v); it is unset at %s in any case", flag, err, timeOrUnknown(a.Status.ExpiresAt)), 1024)
			return ctrl.Result{RequeueAfter: cephActionPoll}, nil
		}
		a.Status.Message = clipString(fmt.Sprintf("%s set until %s%s", flag, timeOrUnknown(a.Status.ExpiresAt), driftNote(err)), 1024)
		return ctrl.Result{RequeueAfter: cephActionPoll}, nil
	}
	// Active.
	if !a.Spec.Release && now.Before(a.Status.ExpiresAt.Time) {
		left := a.Status.ExpiresAt.Sub(now)
		return ctrl.Result{RequeueAfter: min(left, 5*time.Minute)}, nil
	}
	why := "its time is up"
	if a.Spec.Release {
		why = "released"
	}
	if a.Status.FlagWasSet {
		return s.end(framev1beta1.CephActionDone, fmt.Sprintf("%s: %s left set (it was set before this action)", why, flag))
	}
	if a.Status.UnsetSentAt == nil {
		a.Status.UnsetSentAt = s.nowTime()
	}
	// Unsetting is safe to resend: retried until Ceph says it is done.
	_, err := s.ceph.SetFlag(ctx, flag, false)
	if err != nil && !isFlagDrift(err) {
		return s.wait(fmt.Sprintf("%s: unsetting %s failed, retrying: %v", why, flag, err))
	}
	return s.end(framev1beta1.CephActionDone, fmt.Sprintf("%s: %s unset%s", why, flag, driftNote(err)))
}

// driftNote reports other flags that moved during a SetFlag ("" if none).
func driftNote(err error) string {
	if err == nil {
		return ""
	}
	return "; but " + err.Error()
}

func timeOrUnknown(t *metav1.Time) string {
	if t == nil {
		return "an unknown time"
	}
	return t.UTC().Format(time.RFC3339)
}

func (r *FrameCephActionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameCephAction{}).
		Named("framecephaction").
		Complete(r)
}
