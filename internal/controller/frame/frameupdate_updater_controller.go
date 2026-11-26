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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/updater"
)

const (
	// updaterDeployment and updaterContainer are the updater's own workload.
	updaterDeployment = "frame-updater"
	updaterContainer  = "updater"
	// updaterReadyDeadline is how long the new updater has to become ready
	// before the operator puts the previous image back.
	updaterReadyDeadline = 5 * time.Minute
	// updaterStableFor is how long the new updater must stay ready before the
	// update is called done: a process that dies at its first reconcile is
	// ready for a few seconds first.
	updaterStableFor = time.Minute
	updaterPoll      = 5 * time.Second
)

// FrameUpdateUpdaterReconciler applies the updater's own update. The updater
// cannot: the process that drives an update would be replaced in the middle of
// it. So the updater asks (FrameUpdate.status.updater.to), this controller
// changes the image of the one Deployment, watches it become ready, and puts the
// previous image back if it does not. It is the operator's half of the cross
// supervision of spec section 4.1; the updater applies the operator.
//
// It only ever changes the image of the updater container, and only to a digest.
type FrameUpdateUpdaterReconciler struct {
	client.Client
	// Namespace is where the updater runs; requests elsewhere are ignored.
	Namespace string
	Now       func() time.Time
	// ReadyDeadline, StableFor and Poll default to the constants below; tests
	// shorten them.
	ReadyDeadline, StableFor, Poll time.Duration
}

func (r *FrameUpdateUpdaterReconciler) readyDeadline() time.Duration {
	if r.ReadyDeadline > 0 {
		return r.ReadyDeadline
	}
	return updaterReadyDeadline
}

func (r *FrameUpdateUpdaterReconciler) stableFor() time.Duration {
	if r.StableFor > 0 {
		return r.StableFor
	}
	return updaterStableFor
}

func (r *FrameUpdateUpdaterReconciler) poll() time.Duration {
	if r.Poll > 0 {
		return r.Poll
	}
	return updaterPoll
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameupdates,verbs=get;list;watch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framereleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameupdates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch

func (r *FrameUpdateUpdaterReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile advances the updater's self-update by one step.
//
// The journal is the status: every state is written before the action it
// announces, so a restart of the operator at any point resumes, never repeats a
// step that was not asked for.
//
//	Pending     -> Applying (From and Since written, then the image changed)
//	Applying    -> Ready (rolled out and stable) | RollingBack (deadline)
//	RollingBack -> RolledBack (From put back)
func (r *FrameUpdateUpdaterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Namespace != r.Namespace {
		return ctrl.Result{}, nil
	}
	var upd framev1beta1.FrameUpdate
	if err := r.Get(ctx, req.NamespacedName, &upd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	st := upd.Status.Updater
	if st == nil || updaterDecided(st.State) {
		return ctrl.Result{}, nil
	}
	if msg, err := r.refusal(ctx, &upd); err != nil {
		return ctrl.Result{}, err
	} else if msg != "" {
		return ctrl.Result{}, r.fail(ctx, &upd, msg)
	}

	var dep appsv1.Deployment
	key := types.NamespacedName{Namespace: r.Namespace, Name: updaterDeployment}
	if err := r.Get(ctx, key, &dep); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.fail(ctx, &upd, "this install has no "+updaterDeployment+" Deployment")
		}
		return ctrl.Result{}, err
	}
	idx := -1
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == updaterContainer {
			idx = i
		}
	}
	if idx < 0 {
		return ctrl.Result{}, r.fail(ctx, &upd, "the "+updaterDeployment+" Deployment has no "+updaterContainer+" container")
	}
	current := dep.Spec.Template.Spec.Containers[idx].Image
	now := metav1.NewTime(r.now())
	wait := ctrl.Result{RequeueAfter: r.poll()}

	switch st.State {
	case "", framev1beta1.UpdaterPending:
		if current == st.To {
			return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
				s.From = current
				s.State = framev1beta1.UpdaterReady
				s.Message = "the updater already runs this image"
			})
		}
		// Say what to go back to before anything is changed: a restart between
		// the two writes must still know it.
		if err := r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
			s.From = current
			s.State = framev1beta1.UpdaterApplying
			s.Since = &now
		}); err != nil {
			return ctrl.Result{}, err
		}
		return wait, r.setImage(ctx, &dep, idx, st.To)

	case framev1beta1.UpdaterApplying:
		return r.applying(ctx, &upd, &dep, idx)

	case framev1beta1.UpdaterRollingBack:
		if current != st.From {
			if err := r.setImage(ctx, &dep, idx, st.From); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
			s.State = framev1beta1.UpdaterRolledBack
			s.Message = fmt.Sprintf("the new updater was not ready after %s: the previous image is back", r.readyDeadline())
		})
	}
	return ctrl.Result{}, nil
}

// refusal says why this request must not be acted on, "" when it may.
func (r *FrameUpdateUpdaterReconciler) refusal(ctx context.Context, upd *framev1beta1.FrameUpdate) (string, error) {
	if !updater.IsPinnedImage(upd.Status.Updater.To) {
		return "the updater image is not pinned by digest: refused", nil
	}
	// Only the newest request that still has an updater to apply acts: an older
	// one would put an obsolete image back.
	newer, err := r.newerRequestPending(ctx, upd)
	if err != nil || newer {
		return "replaced by a newer update", err
	}
	// What is asked must be what a verified release names: the updater's word is
	// not enough to make the operator install an image.
	ok, err := r.namedByVerifiedRelease(ctx, upd)
	if err != nil || !ok {
		return "the updater image is not the one a verified release of " + upd.Spec.TargetVersion + " names: refused", err
	}
	return "", nil
}

func (r *FrameUpdateUpdaterReconciler) fail(ctx context.Context, upd *framev1beta1.FrameUpdate, msg string) error {
	return r.setUpdater(ctx, upd, func(s *framev1beta1.UpdaterStatus) {
		s.State = framev1beta1.UpdaterFailed
		s.Message = msg
	})
}

// applying watches the new image until it is stable, or rolls it back.
func (r *FrameUpdateUpdaterReconciler) applying(ctx context.Context, upd *framev1beta1.FrameUpdate, dep *appsv1.Deployment, idx int) (ctrl.Result, error) {
	st := upd.Status.Updater
	current := dep.Spec.Template.Spec.Containers[idx].Image
	now := metav1.NewTime(r.now())
	wait := ctrl.Result{RequeueAfter: r.poll()}
	switch current {
	case st.From:
		// Cut short between the journal and the image: apply it now, with a
		// fresh deadline.
		if err := r.setUpdater(ctx, upd, func(s *framev1beta1.UpdaterStatus) { s.Since = &now }); err != nil {
			return ctrl.Result{}, err
		}
		return wait, r.setImage(ctx, dep, idx, st.To)
	case st.To:
	default:
		return ctrl.Result{}, r.fail(ctx, upd, "the "+updaterDeployment+" image was changed by someone else during the update: left as it is")
	}
	if healthy(dep) {
		if st.ReadySince == nil {
			return wait, r.setUpdater(ctx, upd, func(s *framev1beta1.UpdaterStatus) { s.ReadySince = &now })
		}
		if r.now().Sub(st.ReadySince.Time) >= r.stableFor() {
			return ctrl.Result{}, r.setUpdater(ctx, upd, func(s *framev1beta1.UpdaterStatus) {
				s.State = framev1beta1.UpdaterReady
				s.Message = ""
			})
		}
		return wait, nil
	}
	if st.ReadySince != nil {
		// It was ready and no longer is: start counting again.
		return wait, r.setUpdater(ctx, upd, func(s *framev1beta1.UpdaterStatus) { s.ReadySince = nil })
	}
	if st.Since != nil && r.now().Sub(st.Since.Time) <= r.readyDeadline() {
		return wait, nil
	}
	if st.From == "" {
		return ctrl.Result{}, r.fail(ctx, upd, "the new updater did not become ready and the previous image is unknown: check the "+
			updaterDeployment+" Deployment by hand")
	}
	return wait, r.setUpdater(ctx, upd, func(s *framev1beta1.UpdaterStatus) {
		s.State = framev1beta1.UpdaterRollingBack
		s.Message = fmt.Sprintf("the new updater was not ready after %s", r.readyDeadline())
	})
}

func updaterDecided(state string) bool {
	switch state {
	case framev1beta1.UpdaterReady, framev1beta1.UpdaterRolledBack, framev1beta1.UpdaterFailed:
		return true
	}
	return false
}

// newerRequestPending: another request in this namespace, created later, still
// has an updater to apply.
func (r *FrameUpdateUpdaterReconciler) newerRequestPending(ctx context.Context, u *framev1beta1.FrameUpdate) (bool, error) {
	var l framev1beta1.FrameUpdateList
	if err := r.List(ctx, &l, client.InNamespace(r.Namespace)); err != nil {
		return false, err
	}
	for i := range l.Items {
		o := &l.Items[i]
		if o.Name == u.Name || o.Status.Updater == nil || updaterDecided(o.Status.Updater.State) {
			continue
		}
		if u.CreationTimestamp.Before(&o.CreationTimestamp) ||
			(u.CreationTimestamp.Equal(&o.CreationTimestamp) && u.Name < o.Name) {
			return true, nil
		}
	}
	return false, nil
}

// namedByVerifiedRelease: the image asked for is the updater image of the
// verified release this request targets.
func (r *FrameUpdateUpdaterReconciler) namedByVerifiedRelease(ctx context.Context, u *framev1beta1.FrameUpdate) (bool, error) {
	var l framev1beta1.FrameReleaseList
	if err := r.List(ctx, &l, client.InNamespace(r.Namespace)); err != nil {
		return false, err
	}
	for i := range l.Items {
		fr := &l.Items[i]
		if fr.Spec.Version == u.Spec.TargetVersion && fr.Status.Verified && !fr.Spec.Revoked {
			return fr.Spec.Images[updater.UpdaterComponent] == u.Status.Updater.To, nil
		}
	}
	return false, nil
}

// healthy is true once the Deployment's newest ReplicaSet is the only one and
// every wanted replica is ready and available. No replica wanted is not healthy.
func healthy(d *appsv1.Deployment) bool {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	return want > 0 && s.ObservedGeneration >= d.Generation &&
		s.UpdatedReplicas == want && s.Replicas == want && s.ReadyReplicas == want && s.AvailableReplicas >= want
}

// setImage changes the image of the updater container and nothing else.
func (r *FrameUpdateUpdaterReconciler) setImage(ctx context.Context, d *appsv1.Deployment, idx int, image string) error {
	base := d.DeepCopy()
	d.Spec.Template.Spec.Containers[idx].Image = image
	return r.Patch(ctx, d, client.StrategicMergeFrom(base))
}

func (r *FrameUpdateUpdaterReconciler) setUpdater(ctx context.Context, u *framev1beta1.FrameUpdate, mut func(*framev1beta1.UpdaterStatus)) error {
	base := u.DeepCopy()
	mut(u.Status.Updater)
	return r.Status().Patch(ctx, u, client.MergeFrom(base))
}

// SetupWithManager registers the controller.
func (r *FrameUpdateUpdaterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameUpdate{}).
		Named("frameupdate-updater-self").
		Complete(r)
}
