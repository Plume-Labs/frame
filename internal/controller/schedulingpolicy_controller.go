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
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

const schedulingPolicyFinalizer = "frame.plume-labs.io/schedulingpolicy"

// SchedulingPolicyReconciler reconciles a SchedulingPolicy object
type SchedulingPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *SchedulingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var sp framev1alpha1.SchedulingPolicy
	if err := r.Get(ctx, req.NamespacedName, &sp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sp.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &sp)
	}

	if !controllerutil.ContainsFinalizer(&sp, schedulingPolicyFinalizer) {
		controllerutil.AddFinalizer(&sp, schedulingPolicyFinalizer)
		return ctrl.Result{}, r.Update(ctx, &sp)
	}

	schedulerNS := schedulerNamespace(sp.Spec.Scheduler)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "frame-policy-" + sp.Name,
			Namespace: schedulerNS,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = map[string]string{
			"frame.plume-labs.io/policy":    sp.Name,
			"frame.plume-labs.io/scheduler": sp.Spec.Scheduler,
		}
		cm.Data = map[string]string{
			"scheduler":      sp.Spec.Scheduler,
			"queue":          sp.Spec.QueueName,
			"priorityClass":  sp.Spec.PriorityClass,
			"gangScheduling": strconv.FormatBool(sp.Spec.GangScheduling),
			"preemption":     strconv.FormatBool(sp.Spec.Preemption),
		}
		return nil
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("syncing policy ConfigMap: %w", err)
	}

	patch := client.MergeFrom(sp.DeepCopy())
	setCondition(&sp.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Applied",
		Message:            fmt.Sprintf("Policy synced to %s/%s", schedulerNS, cm.Name),
		ObservedGeneration: sp.Generation,
	})
	log.Info("Reconciled SchedulingPolicy", "scheduler", sp.Spec.Scheduler, "queue", sp.Spec.QueueName)
	return ctrl.Result{}, r.Status().Patch(ctx, &sp, patch)
}

func (r *SchedulingPolicyReconciler) reconcileDelete(ctx context.Context, sp *framev1alpha1.SchedulingPolicy) (ctrl.Result, error) {
	ns := schedulerNamespace(sp.Spec.Scheduler)
	cm := &corev1.ConfigMap{}
	cm.Name = "frame-policy-" + sp.Name
	cm.Namespace = ns
	if err := r.Delete(ctx, cm); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(sp, schedulingPolicyFinalizer)
	return ctrl.Result{}, r.Update(ctx, sp)
}

func schedulerNamespace(scheduler string) string {
	switch scheduler {
	case "volcano":
		return "volcano-system"
	case "yunikorn":
		return "yunikorn"
	default:
		return "kube-system"
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *SchedulingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.SchedulingPolicy{}).
		Named("schedulingpolicy").
		Complete(r)
}
