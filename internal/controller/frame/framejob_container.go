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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/scheduling"
)

// containerObjectGVK is the primitive spec.type maps a container-substrate
// FrameJob onto (design doc section 3.2's table): realtime and background
// both land on batch/v1, batch lands on Volcano's Job for gang scheduling
// and queue quota.
func containerObjectGVK(t framev1beta1.WorkloadType) schema.GroupVersionKind {
	if t == framev1beta1.WorkloadTypeBatch {
		return volcanoJobGVK
	}
	return batchJobGVK
}

// schedulingPolicyNameForType is the convention-based link between a
// FrameJob's type and the SchedulingPolicy that supplies its Volcano queue.
//
// Design decision (design doc section 3.2, "Le lien manquant", and section
// 6's risk on the same point): FrameJob does not gain a
// spec.schedulingPolicyRef field to name its SchedulingPolicy explicitly,
// even though the design doc flags that as the cleaner option. Every field
// on FrameJobSpec is frozen once shipped — docs/upgrading.md: no field may
// later be renamed, removed, or have its meaning tightened — and this stage
// does not yet know whether "one SchedulingPolicy per type" is even the
// right cardinality long-term (a caller might reasonably want a different
// queue per job, not per type, which a single ref field wouldn't obviously
// solve either). A naming convention costs nothing to change or replace
// later: it lives entirely in this function, not in the API. An explicit ref
// field shipped now and wrong later would be permanent frozen surface. The
// trade-off taken: correctness tied to a naming convention over an unwritten
// CRD, in exchange for keeping the frozen surface exactly as small as stage
// 1 left it. If this proves wrong, the fix is additive — a new optional
// spec.schedulingPolicyRef, preferred over the convention when set — not a
// breaking change.
func schedulingPolicyNameForType(t framev1beta1.WorkloadType) string {
	return "frame-" + string(t)
}

// buildContainerObject builds the primitive spec.type names for a
// container-substrate FrameJob's spec.container. For the batch type it also
// resolves the SchedulingPolicy that supplies the Volcano queue and returns
// a SchedulingPolicyResolved condition recording whether that resolution
// succeeded — the "degrade to the default scheduler and say so" requirement
// from the design doc's risk list: a caller reading only the created
// object's spec.queue cannot tell "no queue configured" from "no
// SchedulingPolicy exists at all", but the condition says which.
func (r *FrameJobReconciler) buildContainerObject(ctx context.Context, job *framev1beta1.FrameJob) (*unstructured.Unstructured, *metav1.Condition, error) {
	c := job.Spec.Container
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{
			Name:      "main",
			Image:     c.Image,
			Command:   c.Command,
			Args:      c.Args,
			Env:       c.Env,
			EnvFrom:   c.EnvFrom,
			Resources: c.Resources,
		}},
	}
	podSpecUnstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&podSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("converting pod spec: %w", err)
	}

	// labels starts from the FrameJob's own metadata.labels — this is what
	// lets Neura's JobWatcher (and anything else selecting by label) find
	// the Job it submitted, the correlation GAP 2 exists to restore. Frame's
	// own labels are applied second, into the same map, so they win any key
	// collision: a caller must not be able to overwrite
	// frame.plume-labs.io/job or /job-namespace and redirect which FrameJob
	// this object's events are attributed to (workflowToFrameJob below reads
	// exactly those two keys to route watch events back to a FrameJob).
	labels := map[string]any{}
	for k, v := range job.Labels {
		labels[k] = v
	}
	for k, v := range map[string]string{
		"frame.plume-labs.io/job":           job.Name,
		"frame.plume-labs.io/job-namespace": job.Namespace,
		"frame.plume-labs.io/service-class": string(job.Spec.ServiceClass),
		// buildWorkflow's labels carry frame.plume-labs.io/pipeline in the
		// same slot; a container job has no pipeline, so this carries the
		// substrate instead — the one thing that actually distinguishes
		// realtime from background once priority is (deliberately) left out
		// of it below.
		"frame.plume-labs.io/workload-type": string(job.Spec.Type),
	} {
		labels[k] = v
	}

	if job.Spec.Type == framev1beta1.WorkloadTypeBatch {
		return r.buildVolcanoJob(ctx, job, podSpecUnstructured, labels)
	}
	return buildBatchJob(job, podSpecUnstructured, labels), nil, nil
}

// buildBatchJob is the primitive for realtime and background: both are
// plain batch/v1 Jobs on the default scheduler, and nothing here forces a
// PriorityClass onto one or the other. The design table's "PriorityClass
// haute" for realtime and "priorité basse" for background describe the
// typical case, not a rule this function enforces — priority and substrate
// are deliberately orthogonal (design section 3.1: "elles restent
// indépendantes … un batch nocturne peut être HIGH-tier et non urgent").
// spec.priority alone decides the PriorityClass, through the exact same
// scheduling.PriorityClassForJobPriority every other substrate — including
// the pipeline path's buildWorkflow — already uses, so a caller cannot get a
// different answer by picking a different substrate. The
// frame.plume-labs.io/workload-type label above is what actually lets a
// human or a metrics pipeline tell realtime and background apart.
func buildBatchJob(job *framev1beta1.FrameJob, podSpec map[string]any, labels map[string]any) *unstructured.Unstructured {
	if pc := scheduling.PriorityClassForJobPriority(job.Spec.Priority); pc != "" {
		podSpec["priorityClassName"] = pc
	}

	spec := map[string]any{
		// backoffLimit: 0. ContainerSpec exposes no retry-policy field, and
		// Kubernetes' own unset default (6) would silently retry a failing
		// container six times before Ready ever reports Failed — a caller
		// with no field to see or configure that would just watch the job
		// sit at Submitted/Running for however long six retries take, for
		// reasons nothing in the API surface explains. Fail fast; a caller
		// that wants retries can loop at its own layer, the same way an Argo
		// pipeline's retry policy belongs to the WorkflowTemplate, not to
		// anything spec.parameters exposes either.
		"backoffLimit": int64(0),
		"suspend":      job.Spec.Suspended,
		"template": map[string]any{
			// The pod template carries the same labels as the Job object
			// itself (GAP 2) — a selector watching pods rather than the Job
			// needs the correlation too, and there is no reason for the two
			// to diverge.
			"metadata": map[string]any{
				"labels": labels,
			},
			"spec": podSpec,
		},
	}

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]any{
				"name":      job.Name,
				"namespace": job.Namespace,
				"labels":    labels,
			},
			"spec": spec,
		},
	}
}

// buildVolcanoJob is the primitive for the batch type: gang scheduling
// (minAvailable) and queue quota, the two things a plain batch/v1 Job cannot
// give (design section 3.2). See schedulingPolicyNameForType's doc for the
// convention-vs-explicit-ref trade-off behind the SchedulingPolicy lookup
// below.
func (r *FrameJobReconciler) buildVolcanoJob(ctx context.Context, job *framev1beta1.FrameJob, podSpec map[string]any, labels map[string]any) (*unstructured.Unstructured, *metav1.Condition, error) {
	policyName := schedulingPolicyNameForType(job.Spec.Type)
	var sp framev1beta1.SchedulingPolicy
	getErr := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: job.Namespace}, &sp)

	var queueName string
	var cond metav1.Condition
	switch {
	case getErr == nil && sp.Spec.QueueName != "":
		queueName = sp.Spec.QueueName
		cond = metav1.Condition{
			Type:               conditionTypeSchedulingPolicy,
			Status:             metav1.ConditionTrue,
			Reason:             "Applied",
			Message:            fmt.Sprintf("using queue %q from SchedulingPolicy %q", queueName, policyName),
			ObservedGeneration: job.Generation,
		}
	case getErr == nil:
		// The policy exists but names no queue (QueueName accepts empty —
		// see SchedulingPolicySpec.QueueName's own doc). Same degradation as
		// the not-found case below: no queue means the default scheduler
		// queue, and it must say so rather than let a caller wonder why
		// spec.queue is absent.
		cond = metav1.Condition{
			Type:               conditionTypeSchedulingPolicy,
			Status:             metav1.ConditionFalse,
			Reason:             "NoQueueConfigured",
			Message:            fmt.Sprintf("SchedulingPolicy %q sets no queueName; falling back to Volcano's default queue", policyName),
			ObservedGeneration: job.Generation,
		}
	case apierrors.IsNotFound(getErr):
		cond = metav1.Condition{
			Type:               conditionTypeSchedulingPolicy,
			Status:             metav1.ConditionFalse,
			Reason:             "PolicyNotFound",
			Message:            fmt.Sprintf("no SchedulingPolicy named %q; falling back to Volcano's default queue", policyName),
			ObservedGeneration: job.Generation,
		}
	default:
		return nil, nil, fmt.Errorf("looking up SchedulingPolicy %q: %w", policyName, getErr)
	}

	spec := map[string]any{
		"minAvailable":  int64(1),
		"schedulerName": "volcano",
		"tasks": []any{
			map[string]any{
				"name":     "main",
				"replicas": int64(1),
				"template": map[string]any{
					// Same rationale as buildBatchJob's template.metadata.labels.
					"metadata": map[string]any{
						"labels": labels,
					},
					"spec": podSpec,
				},
			},
		},
	}
	if queueName != "" {
		spec["queue"] = queueName
	}
	if pc := scheduling.PriorityClassForJobPriority(job.Spec.Priority); pc != "" {
		spec["priorityClassName"] = pc
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "batch.volcano.sh/v1alpha1",
			"kind":       "Job",
			"metadata": map[string]any{
				"name":      job.Name,
				"namespace": job.Namespace,
				"labels":    labels,
			},
			"spec": spec,
		},
	}
	return obj, &cond, nil
}

// batchJobPhase maps a batch/v1 Job's status onto the same five-value Ready
// vocabulary workflowPhase produces for a pipeline (Submitted, Running,
// Suspended, Completed, Failed) — Ready.Reason is contractual across every
// substrate, not owned per-substrate.
//
// batch/v1's own vocabulary is condition-based (Complete/Failed conditions
// plus status.active/succeeded/failed counts), not a single phase string the
// way Argo and Volcano both expose, so this reads conditions first and only
// falls back to status.active for the in-between states.
func batchJobPhase(job *unstructured.Unstructured, suspended bool) string {
	conditions, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(cm, "type")
		condStatus, _, _ := unstructured.NestedString(cm, "status")
		if condStatus != "True" {
			continue
		}
		switch condType {
		case "Complete":
			return jobPhaseCompleted
		case "Failed":
			return jobPhaseFailed
		}
	}
	// Neither terminal condition is True yet: suspended (via the field this
	// controller itself synced onto spec.suspend) takes precedence over an
	// active-pod count that may simply not have caught up, exactly the
	// precedence workflowPhase already applies for the pipeline path.
	if suspended {
		return jobPhaseSuspended
	}
	active, _, _ := unstructured.NestedInt64(job.Object, "status", "active")
	if active > 0 {
		return jobPhaseRunning
	}
	return jobPhaseSubmitted
}

// volcanoJobPhase maps a Volcano Job's status.state.phase onto the same
// five-value Ready vocabulary. Volcano's own phase set is wider — Pending,
// Running, Restarting, Completing, Completed, Terminating, Terminated,
// Aborting, Aborted, Failed (volcano.sh/en/docs/vcjob) — so several fold
// onto one Frame phase:
//   - Restarting/Completing fold onto Running: the job is still actively
//     making progress toward a terminal state, which is what Running means
//     to a Frame client; Completing specifically is not yet Completed
//     (the minAvailable pods finished but the rest haven't been reaped).
//   - Terminating/Terminated/Aborting/Aborted fold onto Failed: none of them
//     is a success, and Frame's Ready vocabulary has no "cancelled" reason
//     to add one for — that vocabulary is contractual (clients, including
//     Neura, branch on it), so widening it is its own review, not a side
//     effect of wiring Volcano in.
func volcanoJobPhase(job *unstructured.Unstructured) string {
	phase, _, _ := unstructured.NestedString(job.Object, "status", "state", "phase")
	switch phase {
	case "Completed":
		return jobPhaseCompleted
	case "Failed", "Terminating", "Terminated", "Aborting", "Aborted":
		return jobPhaseFailed
	case "Running", "Restarting", "Completing":
		return jobPhaseRunning
	default: // "Pending", "", unknown
		return jobPhaseSubmitted
	}
}

// containerObjectMessage extracts a human-readable status message from a
// created container primitive. batch/v1 doesn't carry a single status
// message field the way Argo Workflows and Volcano Jobs both do
// (status.message / status.state.message) — its own closest analog is a
// terminal condition's message.
func containerObjectMessage(obj *unstructured.Unstructured, gvk schema.GroupVersionKind) string {
	if gvk.Group == volcanoJobGVK.Group {
		msg, _, _ := unstructured.NestedString(obj.Object, "status", "state", "message")
		return msg
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if status, _, _ := unstructured.NestedString(cm, "status"); status == "True" {
			if msg, _, _ := unstructured.NestedString(cm, "message"); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// suspendAppliedReason is the SuspendApplied condition's Reason, or "" when
// unset — the Volcano-path analog of readyReason.
func suspendAppliedReason(conditions []metav1.Condition) string {
	if c := meta.FindStatusCondition(conditions, conditionTypeSuspendApplied); c != nil {
		return c.Reason
	}
	return ""
}

// reconcileContainer is stage 2's dispatch for a container-substrate
// FrameJob. It creates the primitive containerObjectGVK names for spec.type
// and drives status off it exactly the way the pipeline path (Reconcile's
// body below the dispatch in framejob_controller.go) drives status off its
// Argo Workflow — same finalizer, same ObservedGeneration/phase-changed
// gating, same event/metric firing on a phase transition. It is a sibling
// function rather than a shared refactor of that logic; see the dispatch
// comment in Reconcile for why.
func (r *FrameJobReconciler) reconcileContainer(ctx context.Context, job *framev1beta1.FrameJob) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	ns := job.Namespace
	gvk := containerObjectGVK(job.Spec.Type)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)
	err := r.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if apierrors.IsNotFound(err) {
		if job.Spec.Suspended {
			// Honor Suspended before creation for every container substrate,
			// including batch/Volcano where there is no live pause (see
			// volcanoJobPhase's doc) — creation-time is the one point in the
			// lifecycle where "suspended" and "not yet created" coincide for
			// all three types, so supporting it here costs nothing and needs
			// no substrate-specific primitive.
			patch := client.MergeFrom(job.DeepCopy())
			job.Status.ObservedGeneration = job.Generation
			setJobReady(job, jobPhaseSuspended, "creation held: spec.suspended is true")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Patch(ctx, job, patch)
		}

		obj, schedCond, buildErr := r.buildContainerObject(ctx, job)
		if buildErr != nil {
			return ctrl.Result{}, fmt.Errorf("building container object: %w", buildErr)
		}
		if err := r.Create(ctx, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating %s.%s: %w", gvk.Kind, gvk.Group, err)
		}
		r.Recorder.Event(job, corev1.EventTypeNormal, "WorkloadCreated", fmt.Sprintf("%s.%s %s/%s created", gvk.Kind, gvk.Group, ns, job.Name))
		log.Info("Created container workload", "kind", gvk.Kind, "group", gvk.Group, "name", job.Name, "namespace", ns)

		patch := client.MergeFrom(job.DeepCopy())
		job.Status.ObservedGeneration = job.Generation
		job.Status.ContainerJobName = job.Name
		job.Status.ContainerJobKind = gvk.Kind + "." + gvk.Group
		now := metav1.Now()
		job.Status.StartTime = &now
		if schedCond != nil {
			meta.SetStatusCondition(&job.Status.Conditions, *schedCond)
		}
		setJobReady(job, jobPhaseSubmitted, fmt.Sprintf("%s.%s %s/%s created", gvk.Kind, gvk.Group, ns, job.Name))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Patch(ctx, job, patch)
	}

	// Existing object: sync suspend where the substrate supports a live
	// primitive for it, then derive phase. Computed before any mutation so
	// the phaseChanged/suspendChanged comparison below reads the pre-update
	// status, the same discipline the pipeline path's Reconcile body uses.
	var phase string
	var suspendStatus metav1.ConditionStatus
	var suspendReason, suspendMsg string
	isVolcano := job.Spec.Type == framev1beta1.WorkloadTypeBatch
	if isVolcano {
		phase = volcanoJobPhase(existing)
		if job.Spec.Suspended {
			// Volcano has no live pause/resume primitive comparable to
			// batch/v1's spec.suspend once a Job has started — tracked,
			// unresolved: github.com/volcano-sh/volcano#3875 proposes one,
			// and the existing abort-based workaround is documented there
			// (via #3067) as unreliable enough that landing it on a job
			// that's actually running is worse than doing nothing. Ready
			// keeps reporting the real derived phase rather than lying
			// Suspended; this condition is the honest "I heard you, I can't"
			// signal a client that only reads Ready would otherwise miss.
			suspendStatus = metav1.ConditionFalse
			suspendReason = "VolcanoLiveSuspendUnsupported"
			suspendMsg = "spec.suspended=true has no effect on a Volcano Job that has already started; Volcano has no live pause primitive (volcano-sh/volcano#3875)"
		} else {
			suspendStatus = metav1.ConditionTrue
			suspendReason = "NotRequested"
		}
	} else {
		if err := r.syncSuspend(ctx, existing, job.Spec.Suspended); err != nil {
			log.Error(err, "Failed to sync suspend field")
		}
		phase = batchJobPhase(existing, job.Spec.Suspended)
	}

	phaseChanged := readyReason(job.Status.Conditions) != phase
	suspendChanged := isVolcano && suspendAppliedReason(job.Status.Conditions) != suspendReason

	if phaseChanged || suspendChanged || job.Status.ObservedGeneration != job.Generation {
		patch := client.MergeFrom(job.DeepCopy())
		job.Status.ObservedGeneration = job.Generation
		if phaseChanged {
			job.Status.Message = containerObjectMessage(existing, gvk)
			if phase == jobPhaseCompleted || phase == jobPhaseFailed {
				now := metav1.Now()
				job.Status.CompletionTime = &now
			}
			setJobReady(job, phase, job.Status.Message)
		}
		if isVolcano {
			meta.SetStatusCondition(&job.Status.Conditions, metav1.Condition{
				Type:               conditionTypeSuspendApplied,
				Status:             suspendStatus,
				Reason:             suspendReason,
				Message:            suspendMsg,
				ObservedGeneration: job.Generation,
			})
		}
		if err := r.Status().Patch(ctx, job, patch); err != nil {
			return ctrl.Result{}, err
		}
		if phaseChanged {
			eventType := corev1.EventTypeNormal
			if phase == jobPhaseFailed {
				eventType = corev1.EventTypeWarning
			}
			r.Recorder.Event(job, eventType, "Phase"+phase, fmt.Sprintf("Job phase changed to %s", phase))
			switch phase {
			case jobPhaseCompleted:
				frameJobCompleted.Inc()
			case jobPhaseFailed:
				frameJobFailed.Inc()
			}
		}
	}

	if phase == jobPhaseCompleted || phase == jobPhaseFailed {
		log.Info("Job terminal", "phase", phase)
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// reconcileContainerDelete deletes the created batch/v1 Job or Volcano Job.
// It mirrors reconcileDelete's Workflow cleanup exactly: no ownerReference
// cascade here either. Frame's FrameJob controller has never used
// controller-runtime's SetControllerReference/owner-GC for its created
// workload — grep confirms the only callers of SetControllerReference in
// this repo are internal/controller/services/binding.go and the inference
// provider — relying instead on the finalizer to delete-by-name the object
// it created before releasing the FrameJob itself. This keeps that same
// contract for the three new kinds rather than introducing a second cleanup
// mechanism (owner refs) that would only cover the new paths and leave the
// pipeline path on the old one.
//
// Deletion is guarded against the target CRD (Volcano's) not being installed
// at all: schedulingpolicy_controller.go's reconcileQueue already treats
// Volcano's own CRDs as optionally absent ("Queue CRD may not be installed —
// degrade rather than hard-fail"), and a cluster that never runs a
// batch-type FrameJob has no reason to have Volcano installed either.
// r.Delete against an unregistered GVK fails client-side with
// meta.IsNoMatchError, not apierrors.IsNotFound — client.IgnoreNotFound does
// not swallow it — so it is handled explicitly here rather than left to
// propagate and block finalizer removal (and therefore FrameJob deletion)
// forever.
func (r *FrameJobReconciler) reconcileContainerDelete(ctx context.Context, job *framev1beta1.FrameJob) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	gvk := containerObjectGVK(job.Spec.Type)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(job.Name)
	obj.SetNamespace(job.Namespace)
	if err := r.Delete(ctx, obj); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			// already gone
		case meta.IsNoMatchError(err):
			log.Info("Container workload CRD not installed; nothing to clean up", "kind", gvk.Kind, "group", gvk.Group)
		default:
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(job, frameJobFinalizer)
	return ctrl.Result{}, r.Update(ctx, job)
}
