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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
)

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
//     degraded; reconciles are serial, so two outs cannot both pass;
//   - a flag is set for a while and unset at its end or on release, unless
//     it was set before (by hand); a second object for a flag already held
//     is refused, so one object owns each flag.
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

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framecephactions,verbs=get;list;watch
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
}

func (r *FrameCephActionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var a framev1beta1.FrameCephAction
	if err := r.Get(ctx, req.NamespacedName, &a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if cephActionTerminal(a.Status.Phase) {
		return ctrl.Result{}, nil
	}
	s := &cephActionStep{r: r, a: &a, base: a.DeepCopy()}
	res, err := s.run(ctx)
	if !cephActionStatusEqual(s.base, &a) {
		if perr := r.Status().Patch(ctx, &a, client.MergeFromWithOptions(s.base, client.MergeFromWithOptimisticLock{})); perr != nil {
			return ctrl.Result{}, errors.Join(err, perr)
		}
	}
	return res, err
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
	return nil
}

// stopVerdict says whether one OSD may stop holding its data now.
func (s *cephActionStep) stopVerdict(ctx context.Context, osds []cephmgr.OSDState) (string, error) {
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
		return s.end(framev1beta1.CephActionFailed, fmt.Sprintf("the restart of osd.%d was recorded but %s does not carry it (interrupted?); not retried: create a new restart", id, key.Name))
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
		reader := s.r.APIReader
		if reader == nil {
			reader = s.r.Client
		}
		if err := reader.List(ctx, &all); err != nil {
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
		if _, err := s.ceph.SetFlag(ctx, flag, true); err != nil {
			// Outcome unknown: the end of the duration still unsets it.
			a.Status.Message = clipString(fmt.Sprintf("setting %s failed or its answer was lost (%v); it is unset at %s in any case", flag, err, timeOrUnknown(a.Status.ExpiresAt)), 1024)
			return ctrl.Result{RequeueAfter: cephActionPoll}, nil
		}
		a.Status.Message = fmt.Sprintf("%s set until %s", flag, timeOrUnknown(a.Status.ExpiresAt))
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
	if _, err := s.ceph.SetFlag(ctx, flag, false); err != nil {
		return s.wait(fmt.Sprintf("%s: unsetting %s failed, retrying: %v", why, flag, err))
	}
	return s.end(framev1beta1.CephActionDone, fmt.Sprintf("%s: %s unset", why, flag))
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
