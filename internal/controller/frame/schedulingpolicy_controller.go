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

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

const schedulingPolicyFinalizer = "frame.plume-labs.io/schedulingpolicy"

// SchedulingPolicyReconciler reconciles a SchedulingPolicy object
type SchedulingPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.volcano.sh,resources=queues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=yunikorn.apache.org,resources=queues,verbs=get;list;watch;create;update;patch;delete

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

	var errs []string

	if sp.Spec.PriorityClass != "" {
		if err := r.reconcilePriorityClass(ctx, &sp); err != nil {
			// PriorityClass is a core K8s type — failure is always notable.
			return ctrl.Result{}, fmt.Errorf("reconciling PriorityClass: %w", err)
		}
	}

	if sp.Spec.QueueName != "" && sp.Spec.Scheduler != "default" {
		if err := r.reconcileQueue(ctx, &sp); err != nil {
			// Queue CRD may not be installed — degrade rather than hard-fail.
			log.Info("Queue reconcile failed (scheduler CRD may not be installed)",
				"scheduler", sp.Spec.Scheduler, "queue", sp.Spec.QueueName, "err", err)
			errs = append(errs, fmt.Sprintf("Queue: %v", err))
			r.Recorder.Event(&sp, corev1.EventTypeWarning, "QueueCRDMissing",
				fmt.Sprintf("%s queue CRD not installed: %v", sp.Spec.Scheduler, err))
		}
	}

	patch := client.MergeFrom(sp.DeepCopy())
	if len(errs) > 0 {
		setCondition(&sp.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileError",
			Message:            fmt.Sprintf("%v", errs),
			ObservedGeneration: sp.Generation,
		})
	} else {
		setCondition(&sp.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "Applied",
			Message:            appliedMsg(&sp),
			ObservedGeneration: sp.Generation,
		})
		r.Recorder.Event(&sp, corev1.EventTypeNormal, "Applied", appliedMsg(&sp))
		schedulingPolicyApplied.Inc()
	}
	log.Info("Reconciled SchedulingPolicy", "scheduler", sp.Spec.Scheduler,
		"priorityClass", sp.Spec.PriorityClass, "queue", sp.Spec.QueueName)
	return ctrl.Result{}, r.Status().Patch(ctx, &sp, patch)
}

func (r *SchedulingPolicyReconciler) reconcilePriorityClass(ctx context.Context, sp *framev1alpha1.SchedulingPolicy) error {
	val := int32(0)
	if sp.Spec.PriorityValue != nil {
		val = *sp.Spec.PriorityValue
	}
	pc := &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{Name: sp.Spec.PriorityClass},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pc, func() error {
		pc.Value = val
		pc.GlobalDefault = false
		pc.Description = fmt.Sprintf("Managed by SchedulingPolicy %s/%s", sp.Namespace, sp.Name)
		if pc.Labels == nil {
			pc.Labels = map[string]string{}
		}
		pc.Labels["frame.plume-labs.io/policy-namespace"] = sp.Namespace
		pc.Labels["frame.plume-labs.io/policy-name"] = sp.Name
		pc.Labels["frame.plume-labs.io/scheduler"] = sp.Spec.Scheduler
		pp := corev1.PreemptNever
		if sp.Spec.Preemption {
			pp = corev1.PreemptLowerPriority
		}
		pc.PreemptionPolicy = &pp
		return nil
	})
	return err
}

func (r *SchedulingPolicyReconciler) reconcileQueue(ctx context.Context, sp *framev1alpha1.SchedulingPolicy) error {
	gvk, spec := queueGVKAndSpec(sp)
	q := &unstructured.Unstructured{}
	q.SetGroupVersionKind(gvk)
	q.SetName(sp.Spec.QueueName)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, q, func() error {
		existing, _ := q.Object["spec"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		maps.Copy(existing, spec)
		q.Object["spec"] = existing
		labels := q.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["frame.plume-labs.io/policy-namespace"] = sp.Namespace
		labels["frame.plume-labs.io/policy-name"] = sp.Name
		q.SetLabels(labels)
		return nil
	})
	return err
}

// queueGVKAndSpec returns GVK + spec fields for the scheduler-native queue type.
func queueGVKAndSpec(sp *framev1alpha1.SchedulingPolicy) (schema.GroupVersionKind, map[string]any) {
	weight := int64(1)
	if sp.Spec.QueueWeight != nil {
		weight = int64(*sp.Spec.QueueWeight)
	}
	switch sp.Spec.Scheduler {
	case "volcano":
		return schema.GroupVersionKind{
				Group: "scheduling.volcano.sh", Version: "v1beta1", Kind: "Queue",
			}, map[string]any{
				"weight":      weight,
				"reclaimable": sp.Spec.Preemption,
			}
	case "yunikorn":
		return schema.GroupVersionKind{
				Group: "yunikorn.apache.org", Version: "v1alpha1", Kind: "Queue",
			}, map[string]any{
				"weight": weight,
				"preemption": map[string]any{
					"allowPreemptSelf": sp.Spec.Preemption,
				},
			}
	default:
		// caller guards on scheduler != "default"
		return schema.GroupVersionKind{}, nil
	}
}

func (r *SchedulingPolicyReconciler) reconcileDelete(ctx context.Context, sp *framev1alpha1.SchedulingPolicy) (ctrl.Result, error) {
	if sp.Spec.PriorityClass != "" {
		pc := &schedulingv1.PriorityClass{}
		pc.Name = sp.Spec.PriorityClass
		if err := r.Delete(ctx, pc); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}

	if sp.Spec.QueueName != "" && sp.Spec.Scheduler != "default" {
		gvk, _ := queueGVKAndSpec(sp)
		q := &unstructured.Unstructured{}
		q.SetGroupVersionKind(gvk)
		q.SetName(sp.Spec.QueueName)
		if err := r.Delete(ctx, q); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(sp, schedulingPolicyFinalizer)
	return ctrl.Result{}, r.Update(ctx, sp)
}

func appliedMsg(sp *framev1alpha1.SchedulingPolicy) string {
	msg := fmt.Sprintf("scheduler=%s", sp.Spec.Scheduler)
	if sp.Spec.PriorityClass != "" {
		msg += fmt.Sprintf(" priorityClass=%s", sp.Spec.PriorityClass)
	}
	if sp.Spec.QueueName != "" {
		msg += fmt.Sprintf(" queue=%s", sp.Spec.QueueName)
	}
	return msg
}

// SetupWithManager sets up the controller with the Manager.
func (r *SchedulingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.SchedulingPolicy{}).
		Named("schedulingpolicy").
		Complete(r)
}
