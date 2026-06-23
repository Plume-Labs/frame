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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

const frameJobFinalizer = "frame.plume-labs.io/framejob"

var argoWorkflowGVK = schema.GroupVersionKind{
	Group:   "argoproj.io",
	Version: "v1alpha1",
	Kind:    "Workflow",
}

// FrameJobReconciler reconciles a FrameJob object
type FrameJobReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framejobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framejobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=argoproj.io,resources=workflows,verbs=get;list;watch;create;update;patch;delete

func (r *FrameJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var job framev1alpha1.FrameJob
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !job.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &job)
	}

	if !controllerutil.ContainsFinalizer(&job, frameJobFinalizer) {
		controllerutil.AddFinalizer(&job, frameJobFinalizer)
		return ctrl.Result{}, r.Update(ctx, &job)
	}

	ns := job.Spec.Namespace

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(argoWorkflowGVK)
	err := r.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		wf := buildWorkflow(&job)
		if err := r.Create(ctx, wf); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating ArgoWorkflow: %w", err)
		}
		r.Recorder.Event(&job, corev1.EventTypeNormal, "WorkflowCreated", fmt.Sprintf("ArgoWorkflow %s/%s created", ns, job.Name))
		log.Info("Created ArgoWorkflow", "name", job.Name, "namespace", ns)

		patch := client.MergeFrom(job.DeepCopy())
		job.Status.Phase = "Submitted"
		job.Status.ArgoWorkflowName = job.Name
		now := metav1.Now()
		job.Status.StartTime = &now
		setCondition(&job.Status.Conditions, metav1.Condition{
			Type:               "Submitted",
			Status:             metav1.ConditionTrue,
			Reason:             "WorkflowCreated",
			Message:            fmt.Sprintf("ArgoWorkflow %s/%s created", ns, job.Name),
			ObservedGeneration: job.Generation,
		})
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Patch(ctx, &job, patch)
	}

	// Sync spec.suspend onto the existing Workflow when it diverges.
	if err := r.syncSuspend(ctx, existing, job.Spec.Suspended); err != nil {
		log.Error(err, "Failed to sync suspend field")
	}

	phase := workflowPhase(existing, job.Spec.Suspended)
	if job.Status.Phase != phase {
		patch := client.MergeFrom(job.DeepCopy())
		job.Status.Phase = phase
		job.Status.Message = workflowMessage(existing)
		if phase == "Completed" || phase == "Failed" {
			now := metav1.Now()
			job.Status.CompletionTime = &now
		}
		if err := r.Status().Patch(ctx, &job, patch); err != nil {
			return ctrl.Result{}, err
		}
		eventType := corev1.EventTypeNormal
		if phase == "Failed" {
			eventType = corev1.EventTypeWarning
		}
		r.Recorder.Event(&job, eventType, "Phase"+phase, fmt.Sprintf("Job phase changed to %s", phase))
		switch phase {
		case "Completed":
			frameJobCompleted.Inc()
		case "Failed":
			frameJobFailed.Inc()
		}
	}

	if phase == "Completed" || phase == "Failed" {
		log.Info("Job terminal", "phase", phase)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *FrameJobReconciler) syncSuspend(ctx context.Context, wf *unstructured.Unstructured, suspended bool) error {
	current, _, _ := unstructured.NestedBool(wf.Object, "spec", "suspend")
	if current == suspended {
		return nil
	}
	patch := client.MergeFrom(wf.DeepCopy())
	if err := unstructured.SetNestedField(wf.Object, suspended, "spec", "suspend"); err != nil {
		return err
	}
	return r.Patch(ctx, wf, patch)
}

func (r *FrameJobReconciler) reconcileDelete(ctx context.Context, job *framev1alpha1.FrameJob) (ctrl.Result, error) {
	ns := job.Spec.Namespace
	wf := &unstructured.Unstructured{}
	wf.SetGroupVersionKind(argoWorkflowGVK)
	wf.SetName(job.Name)
	wf.SetNamespace(ns)
	if err := r.Delete(ctx, wf); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(job, frameJobFinalizer)
	return ctrl.Result{}, r.Update(ctx, job)
}

func buildWorkflow(job *framev1alpha1.FrameJob) *unstructured.Unstructured {
	params := []interface{}{
		map[string]interface{}{"name": "gpu-count", "value": strconv.Itoa(int(job.Spec.GPUCount))},
		map[string]interface{}{"name": "service-class", "value": job.Spec.ServiceClass},
	}
	for k, v := range job.Spec.Parameters {
		params = append(params, map[string]interface{}{"name": k, "value": v})
	}

	labels := map[string]interface{}{
		"frame.plume-labs.io/job":           job.Name,
		"frame.plume-labs.io/job-namespace": job.Namespace,
		"frame.plume-labs.io/pipeline":      job.Spec.Pipeline,
		"frame.plume-labs.io/service-class": job.Spec.ServiceClass,
	}

	spec := map[string]interface{}{
		"workflowTemplateRef": map[string]interface{}{
			"name": job.Spec.Pipeline,
		},
		"arguments": map[string]interface{}{
			"parameters": params,
		},
		"suspend": job.Spec.Suspended,
	}
	if pc := jobPriorityClass(job.Spec.Priority); pc != "" {
		spec["priorityClassName"] = pc
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Workflow",
			"metadata": map[string]interface{}{
				"name":      job.Name,
				"namespace": job.Spec.Namespace,
				"labels":    labels,
			},
			"spec": spec,
		},
	}
}

// jobPriorityClass maps FrameJob priority to the Frame-managed PriorityClass name.
func jobPriorityClass(priority string) string {
	switch priority {
	case "critical":
		return "frame-critical"
	case "high":
		return "frame-high"
	case "medium":
		return "frame-medium"
	case "low":
		return "frame-low"
	default:
		return ""
	}
}

func workflowPhase(wf *unstructured.Unstructured, suspended bool) string {
	phase, _, _ := unstructured.NestedString(wf.Object, "status", "phase")
	if suspended && (phase == "Running" || phase == "") {
		return "Suspended"
	}
	switch phase {
	case "Succeeded":
		return "Completed"
	case "Failed", "Error":
		return "Failed"
	case "Running":
		return "Running"
	default:
		return "Submitted"
	}
}

func workflowMessage(wf *unstructured.Unstructured) string {
	msg, _, _ := unstructured.NestedString(wf.Object, "status", "message")
	return msg
}

// workflowToFrameJob maps an Argo Workflow event back to the owning FrameJob.
func (r *FrameJobReconciler) workflowToFrameJob(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	name := labels["frame.plume-labs.io/job"]
	ns := labels["frame.plume-labs.io/job-namespace"]
	if name == "" || ns == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	wfType := &unstructured.Unstructured{}
	wfType.SetGroupVersionKind(argoWorkflowGVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.FrameJob{}).
		Watches(wfType, handler.EnqueueRequestsFromMapFunc(r.workflowToFrameJob)).
		Named("framejob").
		Complete(r)
}
