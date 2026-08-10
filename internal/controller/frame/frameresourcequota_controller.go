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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const frameResourceQuotaFinalizer = "frame.plume-labs.io/frameresourcequota"

// FrameResourceQuotaReconciler reconciles a FrameResourceQuota object
type FrameResourceQuotaReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameresourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameresourcequotas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameresourcequotas/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

func (r *FrameResourceQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var frq framev1beta1.FrameResourceQuota
	if err := r.Get(ctx, req.NamespacedName, &frq); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !frq.DeletionTimestamp.IsZero() {
		controllerutil.RemoveFinalizer(&frq, frameResourceQuotaFinalizer)
		return ctrl.Result{}, r.Update(ctx, &frq)
	}

	if !controllerutil.ContainsFinalizer(&frq, frameResourceQuotaFinalizer) {
		controllerutil.AddFinalizer(&frq, frameResourceQuotaFinalizer)
		return ctrl.Result{}, r.Update(ctx, &frq)
	}

	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{
		"frame.plume-labs.io/service-class": string(frq.Spec.ServiceClass),
	}); err != nil {
		return ctrl.Result{}, err
	}

	hard := buildResourceList(&frq)
	quotaName := "frame-" + strings.ToLower(string(frq.Spec.ServiceClass))

	projected := make([]corev1.ResourceQuota, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		quota := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name:      quotaName,
				Namespace: ns.Name,
			},
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, quota, func() error {
			quota.Spec.Hard = hard
			return nil
		}); err != nil {
			log.Error(err, "Failed to reconcile ResourceQuota", "namespace", ns.Name)
			return ctrl.Result{}, err
		}
		// CreateOrUpdate returns the object as written, which carries the
		// status the apiserver last computed. Reading it here rather than
		// issuing a second Get keeps this to one round trip per namespace.
		projected = append(projected, *quota)
	}

	patch := client.MergeFrom(frq.DeepCopy())
	frq.Status.ObservedGeneration = frq.Generation
	frq.Status.Namespaces = int32(len(nsList.Items))
	frq.Status.Used = sumQuotaUsage(projected)
	meta.SetStatusCondition(&frq.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("Applied to %d namespaces", len(nsList.Items)),
		ObservedGeneration: frq.Generation,
	})
	r.Recorder.Event(&frq, corev1.EventTypeNormal, "QuotaApplied",
		fmt.Sprintf("ResourceQuota applied to %d namespaces for serviceClass=%s", len(nsList.Items), frq.Spec.ServiceClass))
	log.Info("Reconciled FrameResourceQuota", "serviceClass", frq.Spec.ServiceClass, "namespaces", len(nsList.Items))
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, r.Status().Patch(ctx, &frq, patch)
}

func buildResourceList(frq *framev1beta1.FrameResourceQuota) corev1.ResourceList {
	hard := corev1.ResourceList{}
	if frq.Spec.MaxCPU != nil {
		hard[corev1.ResourceLimitsCPU] = *frq.Spec.MaxCPU
	}
	if frq.Spec.MaxMemory != nil {
		hard[corev1.ResourceLimitsMemory] = *frq.Spec.MaxMemory
	}
	if frq.Spec.MaxGPUs > 0 {
		hard[corev1.ResourceName("requests.nvidia.com/gpu")] = *resource.NewQuantity(int64(frq.Spec.MaxGPUs), resource.DecimalSI)
	}
	if frq.Spec.MaxJobs > 0 {
		// Object-count quota on the FrameJob resource itself. Quoting pods here
		// would have capped the pods an ArgoWorkflow fans out to, not the jobs:
		// a single FrameJob can exhaust a pod quota on its own.
		hard[corev1.ResourceName("count/framejobs.frame.plume-labs.io")] =
			*resource.NewQuantity(int64(frq.Spec.MaxJobs), resource.DecimalSI)
	}
	return hard
}

// sumQuotaUsage adds up status.used across every projected ResourceQuota.
// The apiserver computes each one; Frame only aggregates, so a key that no
// namespace reports is absent rather than zero — "not measured" and "measured
// as nothing" are different answers and the UI shows them differently.
func sumQuotaUsage(quotas []corev1.ResourceQuota) corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, q := range quotas {
		for name, qty := range q.Status.Used {
			if existing, ok := total[name]; ok {
				existing.Add(qty)
				total[name] = existing
				continue
			}
			total[name] = qty.DeepCopy()
		}
	}
	if len(total) == 0 {
		return nil
	}
	return total
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameResourceQuotaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameResourceQuota{}).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.namespaceToFRQ)).
		Named("frameresourcequota").
		Complete(r)
}

// namespaceToFRQ enqueues all FrameResourceQuotas whose ServiceClass matches the Namespace's label.
func (r *FrameResourceQuotaReconciler) namespaceToFRQ(ctx context.Context, obj client.Object) []reconcile.Request {
	sc := obj.GetLabels()["frame.plume-labs.io/service-class"]
	if sc == "" {
		return nil
	}
	var list framev1beta1.FrameResourceQuotaList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, frq := range list.Items {
		if string(frq.Spec.ServiceClass) == sc {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: frq.Name, Namespace: frq.Namespace},
			})
		}
	}
	return reqs
}
