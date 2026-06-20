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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

const frameResourceQuotaFinalizer = "frame.plume-labs.io/frameresourcequota"

// FrameResourceQuotaReconciler reconciles a FrameResourceQuota object
type FrameResourceQuotaReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameresourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameresourcequotas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameresourcequotas/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

func (r *FrameResourceQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var frq framev1alpha1.FrameResourceQuota
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
		"frame.plume-labs.io/service-class": frq.Spec.ServiceClass,
	}); err != nil {
		return ctrl.Result{}, err
	}

	hard := buildResourceList(&frq)
	quotaName := "frame-" + strings.ToLower(frq.Spec.ServiceClass)

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
	}

	patch := client.MergeFrom(frq.DeepCopy())
	setCondition(&frq.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("Applied to %d namespaces", len(nsList.Items)),
		ObservedGeneration: frq.Generation,
	})
	log.Info("Reconciled FrameResourceQuota", "serviceClass", frq.Spec.ServiceClass, "namespaces", len(nsList.Items))
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, r.Status().Patch(ctx, &frq, patch)
}

func buildResourceList(frq *framev1alpha1.FrameResourceQuota) corev1.ResourceList {
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
		hard[corev1.ResourcePods] = *resource.NewQuantity(int64(frq.Spec.MaxJobs), resource.DecimalSI)
	}
	return hard
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameResourceQuotaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.FrameResourceQuota{}).
		Named("frameresourcequota").
		Complete(r)
}
