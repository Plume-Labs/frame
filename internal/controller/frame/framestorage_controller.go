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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// usageLabel is the label the content-type webhook selects on. It is
// declared once, here, and read by the PVC webhook too: a second literal
// would be a second policy.
const usageLabel = "frame.plume-labs.io/usage"

// storageResyncInterval is how often an entry's capacity and claim counts
// are refreshed. Capacity is polled, not watched: no backend in this lot
// sends an event when a pool fills up.
const storageResyncInterval = 2 * time.Minute

// FrameStorageReconciler keeps a FrameStorage entry's status current.
//
// It creates nothing destructive and deletes nothing at all. In particular
// it never deletes a PersistentVolume or a PersistentVolumeClaim, in any
// branch: an entry is a description of where volumes may live, and removing
// the description must never remove the volumes.
type FrameStorageReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CephHealth reads the Ceph cluster's health and the reasons behind it.
	// It is a function field rather than a direct client call so the test can
	// drive it without a rook CRD in envtest, and so a cluster with no rook at
	// all is a nil field rather than a permanent error.
	CephHealth func(ctx context.Context) (health string, reasons []string, err error)
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framestorages,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framestorages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framestorages/finalizers,verbs=update
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=ceph.rook.io,resources=cephclusters,verbs=get;list;watch

func (r *FrameStorageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fs framev1beta1.FrameStorage
	if err := r.Get(ctx, req.NamespacedName, &fs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	base := fs.DeepCopy()

	fs.Status.Shared = framev1beta1.SharedForType(fs.Spec.Type)
	fs.Status.ObservedGeneration = fs.Generation

	adopted, err := r.reconcileClass(ctx, &fs)
	if err != nil {
		return ctrl.Result{}, err
	}
	fs.Status.Adopted = adopted

	claims, err := r.countClaims(ctx, fs.Spec.StorageClassName)
	if err != nil {
		return ctrl.Result{}, err
	}
	fs.Status.Claims = claims

	fs.Status.Phase, err = r.reconcileHealth(ctx, &fs)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.reconcileAvailability(&fs)

	if err := r.Status().Patch(ctx, &fs, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching FrameStorage status: %w", err)
	}
	return ctrl.Result{RequeueAfter: storageResyncInterval}, nil
}

// reconcileClass returns whether this entry adopted its StorageClass.
//
// An adopted class gets no owner reference: an owned object is
// garbage-collected with its owner, and adopting a class means explicitly
// not taking that power over it. A class Frame creates does get one, so an
// entry cleans up exactly what it made.
func (r *FrameStorageReconciler) reconcileClass(ctx context.Context, fs *framev1beta1.FrameStorage) (bool, error) {
	var sc storagev1.StorageClass
	err := r.Get(ctx, types.NamespacedName{Name: fs.Spec.StorageClassName}, &sc)
	switch {
	case err == nil:
		return fs.Spec.AdoptExisting, nil
	case !apierrors.IsNotFound(err):
		return false, fmt.Errorf("reading StorageClass %q: %w", fs.Spec.StorageClassName, err)
	}

	provisioner, ok := provisionerFor(fs.Spec.Type)
	if !ok {
		return false, fmt.Errorf("no provisioner known for type %q", fs.Spec.Type)
	}
	created := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: fs.Spec.StorageClassName},
		Provisioner: provisioner,
	}
	if err := ctrl.SetControllerReference(fs, created, r.Scheme); err != nil {
		return false, fmt.Errorf("setting owner on StorageClass: %w", err)
	}
	if err := r.Create(ctx, created); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("creating StorageClass %q: %w", fs.Spec.StorageClassName, err)
	}
	return false, nil
}

func provisionerFor(t string) (string, bool) {
	switch t {
	case "ceph-rbd":
		return "rook-ceph.rbd.csi.ceph.com", true
	case "ceph-bucket":
		return "rook-ceph.ceph.rook.io/bucket", true
	case "local-path":
		return "rancher.io/local-path", true
	default:
		return "", false
	}
}

// countClaims reports how many PVCs provision from this entry's class and
// how many of them carry the usage label. The gap between the two is the
// number the screens display: enforcement is opt-in per object, so an
// entry with many claims and no labels is the normal state, not a fault.
func (r *FrameStorageReconciler) countClaims(ctx context.Context, className string) (*framev1beta1.ClaimCounts, error) {
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs); err != nil {
		return nil, fmt.Errorf("listing PersistentVolumeClaims: %w", err)
	}

	counts := &framev1beta1.ClaimCounts{}
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != className {
			continue
		}
		counts.Total++
		if _, ok := pvc.Labels[usageLabel]; ok {
			counts.Labelled++
		}
	}
	return counts, nil
}

// reconcileHealth returns the entry's phase and sets its Healthy condition.
//
// Only ceph-* entries have a cluster-wide health to report. A local-path
// entry stays Unknown: claiming Ready would assert something nothing
// measured, and this lot exists because of checks that passed for reasons
// other than the ones they named.
func (r *FrameStorageReconciler) reconcileHealth(ctx context.Context, fs *framev1beta1.FrameStorage) (string, error) {
	if !strings.HasPrefix(fs.Spec.Type, "ceph-") || r.CephHealth == nil {
		return "Unknown", nil
	}

	health, reasons, err := r.CephHealth(ctx)
	if err != nil {
		// A health check that could not run leaves the phase Unknown and
		// says why. It never reports Ready: not knowing is not health.
		meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
			Type: "Healthy", Status: metav1.ConditionUnknown, Reason: "CheckFailed",
			Message:            fmt.Sprintf("could not read Ceph health: %v", err),
			ObservedGeneration: fs.Generation,
		})
		return "Unknown", nil
	}

	if health == "HEALTH_OK" {
		meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
			Type: "Healthy", Status: metav1.ConditionTrue, Reason: "CephHealthOK",
			Message: health, ObservedGeneration: fs.Generation,
		})
		return "Ready", nil
	}

	msg := health
	if len(reasons) > 0 {
		msg = fmt.Sprintf("%s: %s", health, strings.Join(reasons, "; "))
	}
	meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
		Type: "Healthy", Status: metav1.ConditionFalse, Reason: "CephDegraded",
		Message: msg, ObservedGeneration: fs.Generation,
	})
	return "Degraded", nil
}

// reconcileAvailability says where this entry can be used. An empty
// spec.nodes means everywhere; a non-empty one names the nodes, and the
// condition carries the list so a screen can render it without re-deriving
// the rule.
func (r *FrameStorageReconciler) reconcileAvailability(fs *framev1beta1.FrameStorage) {
	avail := "all nodes"
	if len(fs.Spec.Nodes) > 0 {
		avail = strings.Join(fs.Spec.Nodes, ", ")
	}
	meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
		Type: "Available", Status: metav1.ConditionTrue, Reason: "Declared",
		Message: avail, ObservedGeneration: fs.Generation,
	})
}

func (r *FrameStorageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameStorage{}).
		Named("framestorage").
		Complete(r)
}
