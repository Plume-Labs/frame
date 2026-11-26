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
	updaterPoll          = 5 * time.Second
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
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameupdates,verbs=get;list;watch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameupdates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch

func (r *FrameUpdateUpdaterReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile advances the updater's self-update by one step.
func (r *FrameUpdateUpdaterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Namespace != r.Namespace {
		return ctrl.Result{}, nil
	}
	var upd framev1beta1.FrameUpdate
	if err := r.Get(ctx, req.NamespacedName, &upd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	st := upd.Status.Updater
	if st == nil {
		return ctrl.Result{}, nil
	}
	switch st.State {
	case framev1beta1.UpdaterReady, framev1beta1.UpdaterRolledBack, framev1beta1.UpdaterFailed:
		return ctrl.Result{}, nil
	}
	if !updater.IsPinnedImage(st.To) {
		return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
			s.State = framev1beta1.UpdaterFailed
			s.Message = "the updater image is not pinned by digest: refused"
		})
	}

	var dep appsv1.Deployment
	key := types.NamespacedName{Namespace: r.Namespace, Name: updaterDeployment}
	if err := r.Get(ctx, key, &dep); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
				s.State = framev1beta1.UpdaterFailed
				s.Message = "this install has no " + updaterDeployment + " Deployment"
			})
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
		return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
			s.State = framev1beta1.UpdaterFailed
			s.Message = "the " + updaterDeployment + " Deployment has no " + updaterContainer + " container"
		})
	}
	current := dep.Spec.Template.Spec.Containers[idx].Image

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
		now := metav1.NewTime(r.now())
		if err := r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
			s.From = current
			s.State = framev1beta1.UpdaterApplying
			s.Since = &now
		}); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setImage(ctx, &dep, idx, st.To); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: updaterPoll}, nil

	case framev1beta1.UpdaterApplying:
		if current != st.To {
			// The image was not applied yet (a restart between the two writes).
			if err := r.setImage(ctx, &dep, idx, st.To); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: updaterPoll}, nil
		}
		if rolledOut(&dep) {
			return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
				s.State = framev1beta1.UpdaterReady
				s.Message = ""
			})
		}
		if st.Since != nil && r.now().Sub(st.Since.Time) > updaterReadyDeadline {
			if !updater.IsPinnedImage(st.From) {
				return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
					s.State = framev1beta1.UpdaterFailed
					s.Message = "the new updater did not become ready and the previous image is unknown: check the " + updaterDeployment + " Deployment by hand"
				})
			}
			if err := r.setImage(ctx, &dep, idx, st.From); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.setUpdater(ctx, &upd, func(s *framev1beta1.UpdaterStatus) {
				s.State = framev1beta1.UpdaterRolledBack
				s.Message = fmt.Sprintf("the new updater was not ready after %s: the previous image is back", updaterReadyDeadline)
			})
		}
		return ctrl.Result{RequeueAfter: updaterPoll}, nil
	}
	return ctrl.Result{}, nil
}

// rolledOut is true once the Deployment's newest ReplicaSet is the only one and
// every replica is ready.
func rolledOut(d *appsv1.Deployment) bool {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	return s.ObservedGeneration >= d.Generation &&
		s.UpdatedReplicas == want && s.Replicas == want && s.ReadyReplicas == want
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
