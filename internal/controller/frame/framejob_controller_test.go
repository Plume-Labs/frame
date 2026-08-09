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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

var _ = Describe("FrameJob Controller", func() {
	const name = "test-job"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	job := &framev1alpha1.FrameJob{}

	BeforeEach(func() {
		*job = framev1alpha1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1alpha1.FrameJobSpec{
				Pipeline:     "neura-training-dag",
				ServiceClass: "HIGH",
				Priority:     "high",
				Namespace:    "default",
				GPUCount:     8,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1alpha1.FrameJob{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.FrameJob{}))
		}, "5s").Should(BeTrue())
	})

	r := func() *FrameJobReconciler {
		return &FrameJobReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, job)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(job, frameJobFinalizer)).To(BeTrue())
	})

	It("creates the backing ArgoWorkflow with the job's spec on second reconcile", func() {
		_, err := r().Reconcile(ctx, req) // add finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req) // creates the backing ArgoWorkflow
		Expect(err).NotTo(HaveOccurred())

		// The Workflow must actually exist, not merely have avoided an error.
		wf := &unstructured.Unstructured{}
		wf.SetGroupVersionKind(argoWorkflowGVK)
		Expect(k8sClient.Get(ctx, key, wf)).To(Succeed())

		priorityClassName, _, _ := unstructured.NestedString(wf.Object, "spec", "priorityClassName")
		Expect(priorityClassName).To(Equal("frame-high"), "priority=high must map to the frame-high PriorityClass")
		params, _, _ := unstructured.NestedSlice(wf.Object, "spec", "arguments", "parameters")
		Expect(params).To(ContainElement(map[string]any{"name": "gpu-count", "value": "8"}))
		Expect(params).To(ContainElement(map[string]any{"name": "service-class", "value": "HIGH"}))
		Expect(wf.GetLabels()["frame.plume-labs.io/job"]).To(Equal(name))
		Expect(wf.GetLabels()["frame.plume-labs.io/job-namespace"]).To(Equal(ns))

		// The FrameJob side of the same reconcile: finalizer retained, and the
		// fields the create branch is supposed to have written are present.
		Expect(k8sClient.Get(ctx, key, job)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(job, frameJobFinalizer)).To(BeTrue())
		Expect(job.Status.ArgoWorkflowName).To(Equal(name))
		Expect(job.Status.StartTime).NotTo(BeNil())
	})

	It("deletes the backing Workflow when the FrameJob is deleted through the reconciler", func() {
		_, err := r().Reconcile(ctx, req) // add finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req) // creates the backing ArgoWorkflow
		Expect(err).NotTo(HaveOccurred())

		wfKey := &unstructured.Unstructured{}
		wfKey.SetGroupVersionKind(argoWorkflowGVK)
		Expect(k8sClient.Get(ctx, key, wfKey)).To(Succeed(), "the Workflow must exist before we can prove it gets deleted")

		Expect(k8sClient.Delete(ctx, job)).To(Succeed())

		_, err = r().Reconcile(ctx, req) // runs reconcileDelete
		Expect(err).NotTo(HaveOccurred())

		wfAfter := &unstructured.Unstructured{}
		wfAfter.SetGroupVersionKind(argoWorkflowGVK)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, wfAfter))).To(BeTrue(),
			"reconcileDelete must delete the backing Workflow, not just drop the finalizer")

		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.FrameJob{}))
		}, "5s").Should(BeTrue(), "removing the finalizer must let the FrameJob itself be garbage-collected")
	})

	It("writes a Ready condition that tracks the workflow, not a write-once Submitted", func() {
		ctx := context.Background()
		name := "cond-tracking"

		job := &framev1alpha1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       framev1alpha1.FrameJobSpec{Pipeline: "neura-training-dag", Namespace: "default"},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, job)
		})

		reconciler := &FrameJobReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		key := types.NamespacedName{Name: name, Namespace: "default"}

		// Pass 1 adds the finalizer, pass 2 creates the Workflow.
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		ready := meta.FindStatusCondition(fetched.Status.Conditions, conditionTypeReady)
		Expect(ready).NotTo(BeNil(), "a FrameJob must carry a Ready condition")
		Expect(ready.Reason).To(Equal(jobPhaseSubmitted))
		Expect(ready.Status).To(Equal(metav1.ConditionFalse), "Submitted is not Ready")
		Expect(meta.FindStatusCondition(fetched.Status.Conditions, "Submitted")).
			To(BeNil(), "the Submitted condition type is gone (F3)")

		// Drive the backing Workflow to Failed and reconcile again.
		wf := &unstructured.Unstructured{}
		wf.SetGroupVersionKind(argoWorkflowGVK)
		Expect(k8sClient.Get(ctx, key, wf)).To(Succeed())
		Expect(unstructured.SetNestedField(wf.Object, "Failed", "status", "phase")).To(Succeed())
		Expect(k8sClient.Update(ctx, wf)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		ready = meta.FindStatusCondition(fetched.Status.Conditions, conditionTypeReady)
		Expect(ready.Reason).To(Equal(jobPhaseFailed),
			"the Ready condition must follow the workflow, not stay at its first value")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
	})
})

var _ = Describe("buildWorkflow", func() {
	makeJob := func(priority string, gpus int32, suspended bool, params map[string]string) *framev1alpha1.FrameJob {
		return &framev1alpha1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "myjob", Namespace: "ctrl-ns"},
			Spec: framev1alpha1.FrameJobSpec{
				Pipeline:     "train-dag",
				ServiceClass: "HIGH",
				Priority:     priority,
				Namespace:    "compute-ns",
				GPUCount:     gpus,
				Suspended:    suspended,
				Parameters:   params,
			},
		}
	}

	It("sets gpu-count and service-class parameters", func() {
		wf := buildWorkflow(makeJob("high", 4, false, nil))
		params, _, _ := unstructured.NestedSlice(wf.Object, "spec", "arguments", "parameters")
		Expect(params).To(ContainElement(map[string]any{"name": "gpu-count", "value": "4"}))
		Expect(params).To(ContainElement(map[string]any{"name": "service-class", "value": "HIGH"}))
	})

	It("wires priorityClassName for known priorities", func() {
		cases := map[string]string{
			"critical": "frame-critical",
			"high":     "frame-high",
			"medium":   "frame-medium",
			"low":      "frame-low",
		}
		for prio, want := range cases {
			wf := buildWorkflow(makeJob(prio, 0, false, nil))
			got, _, _ := unstructured.NestedString(wf.Object, "spec", "priorityClassName")
			Expect(got).To(Equal(want), "priority=%s", prio)
		}
	})

	It("omits priorityClassName when priority is empty", func() {
		wf := buildWorkflow(makeJob("", 0, false, nil))
		_, exists, _ := unstructured.NestedString(wf.Object, "spec", "priorityClassName")
		Expect(exists).To(BeFalse())
	})

	It("sets spec.suspend=true when Suspended=true", func() {
		wf := buildWorkflow(makeJob("high", 2, true, nil))
		suspended, _, _ := unstructured.NestedBool(wf.Object, "spec", "suspend")
		Expect(suspended).To(BeTrue())
	})

	It("sets spec.suspend=false when Suspended=false", func() {
		wf := buildWorkflow(makeJob("high", 2, false, nil))
		suspended, _, _ := unstructured.NestedBool(wf.Object, "spec", "suspend")
		Expect(suspended).To(BeFalse())
	})

	It("stores job-namespace label for reverse mapping", func() {
		wf := buildWorkflow(makeJob("high", 2, false, nil))
		labels := wf.GetLabels()
		Expect(labels["frame.plume-labs.io/job-namespace"]).To(Equal("ctrl-ns"))
		Expect(labels["frame.plume-labs.io/job"]).To(Equal("myjob"))
	})

	It("appends extra parameters from spec.parameters", func() {
		wf := buildWorkflow(makeJob("high", 2, false, map[string]string{"dataset": "s3://bucket/ds"}))
		params, _, _ := unstructured.NestedSlice(wf.Object, "spec", "arguments", "parameters")
		Expect(params).To(ContainElement(map[string]any{"name": "dataset", "value": "s3://bucket/ds"}))
	})
})

var _ = Describe("workflowPhase", func() {
	makeWF := func(phase string) *unstructured.Unstructured {
		wf := &unstructured.Unstructured{Object: map[string]any{}}
		if phase != "" {
			_ = unstructured.SetNestedField(wf.Object, phase, "status", "phase")
		}
		return wf
	}

	It("returns Suspended when suspended=true and workflow not yet terminal", func() {
		Expect(workflowPhase(makeWF("Running"), true)).To(Equal("Suspended"))
		Expect(workflowPhase(makeWF(""), true)).To(Equal("Suspended"))
	})

	It("does not override terminal phases when suspended=true", func() {
		Expect(workflowPhase(makeWF("Succeeded"), true)).To(Equal("Completed"))
		Expect(workflowPhase(makeWF("Failed"), true)).To(Equal("Failed"))
	})

	It("maps Succeeded to Completed", func() {
		Expect(workflowPhase(makeWF("Succeeded"), false)).To(Equal("Completed"))
	})

	It("maps Failed/Error to Failed", func() {
		Expect(workflowPhase(makeWF("Failed"), false)).To(Equal("Failed"))
		Expect(workflowPhase(makeWF("Error"), false)).To(Equal("Failed"))
	})

	It("maps Running to Running", func() {
		Expect(workflowPhase(makeWF("Running"), false)).To(Equal("Running"))
	})

	It("returns Submitted for unknown/empty phase", func() {
		Expect(workflowPhase(makeWF(""), false)).To(Equal("Submitted"))
	})
})

var _ = Describe("workflowToFrameJob", func() {
	It("returns empty when labels missing", func() {
		r := &FrameJobReconciler{}
		wf := &unstructured.Unstructured{}
		wf.SetLabels(map[string]string{})
		Expect(r.workflowToFrameJob(context.Background(), wf)).To(BeEmpty())
	})

	It("maps labels to FrameJob NamespacedName", func() {
		r := &FrameJobReconciler{}
		wf := &unstructured.Unstructured{}
		wf.SetLabels(map[string]string{
			"frame.plume-labs.io/job":           "myjob",
			"frame.plume-labs.io/job-namespace": "ctrl-ns",
		})
		reqs := r.workflowToFrameJob(context.Background(), wf)
		Expect(reqs).To(HaveLen(1))
		Expect(reqs[0].NamespacedName).To(Equal(types.NamespacedName{Name: "myjob", Namespace: "ctrl-ns"}))
	})
})
