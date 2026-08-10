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
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*, not about any controller: the
// enum, the bounds and the defaults introduced in the API freeze are enforced
// by the apiserver from the CRD, so nothing but a real apiserver can show
// them working. They run against the CRDs as kustomize renders them
// (renderedCRDPath), which is the same artefact the cluster installs.
//
// The bounds are the point. A ceiling can be raised after the freeze but
// never introduced, so each one is asserted here by writing an object that
// must be *rejected* — an assertion that the field merely exists would pass
// just as well against a schema with no bounds at all.
var _ = Describe("FrameJob v1beta1 schema", func() {
	// liveShapedJob is modelled field-for-field on neura-embed-refresh, one of
	// the two FrameJobs stored on the test cluster, minus the two fields
	// v1beta1 removes (spec.namespace, status.phase). If v1beta1 cannot hold
	// this, conversion has nowhere to put the objects that exist.
	liveShapedJob := func(name string) *framev1beta1.FrameJob {
		return &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameJobSpec{
				Pipeline:     "neura-inference-dag",
				ServiceClass: framev1beta1.ServiceClassHigh,
				Priority:     "high",
				GPUCount:     0,
				Suspended:    false,
			},
		}
	}

	It("holds a stored object's shape without loss, legacy condition included", func() {
		job := liveShapedJob("live-shaped")
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, job) })

		key := types.NamespacedName{Name: "live-shaped", Namespace: "default"}
		Expect(k8sClient.Get(ctx, key, job)).To(Succeed())

		// The stored objects carry a "Submitted" condition written before the
		// Ready condition existed, alongside the Ready condition Task 3
		// established. Both must survive: v1beta1 does not enumerate
		// condition types, and Task 18 reads Ready's reason to project
		// v1alpha1's status.phase.
		job.Status.ObservedGeneration = 1
		job.Status.ArgoWorkflowName = "neura-embed-refresh"
		job.Status.StartTime = &metav1.Time{Time: metav1.Now().Time}
		job.Status.CompletionTime = &metav1.Time{Time: metav1.Now().Time}
		job.Status.Message = "ArgoWorkflow default/neura-embed-refresh created"
		job.Status.Conditions = []metav1.Condition{
			{
				Type:               "Submitted",
				Status:             metav1.ConditionTrue,
				Reason:             "WorkflowCreated",
				Message:            "ArgoWorkflow default/neura-embed-refresh created",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.Now(),
			},
			{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				Reason:             "Completed",
				Message:            "Workflow succeeded",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.Now(),
			},
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		back := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.Pipeline).To(Equal("neura-inference-dag"))
		Expect(back.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassHigh))
		Expect(back.Spec.Priority).To(Equal("high"))
		Expect(back.Spec.GPUCount).To(BeNumerically("==", 0))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 1))
		Expect(back.Status.ArgoWorkflowName).To(Equal("neura-embed-refresh"))
		Expect(back.Status.StartTime).NotTo(BeNil())
		Expect(back.Status.CompletionTime).NotTo(BeNil())

		byType := map[string]metav1.Condition{}
		for _, c := range back.Status.Conditions {
			byType[c.Type] = c
		}
		Expect(byType).To(HaveKey("Submitted"), "the legacy condition on both stored objects must survive")
		// Task 18 reconstructs v1alpha1's status.phase from exactly this
		// string, and Task 22's SDK reads it directly. If it stops round
		// tripping, both of those break silently.
		Expect(byType["Ready"].Reason).To(Equal("Completed"))
	})

	It("defaults serviceClass, priority, gpuCount and suspended from the schema", func() {
		// v1alpha1 defaulted serviceClass in the mutating webhook. The webhook
		// is not running in this suite, so a LOW here can only have come from
		// the CRD.
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "defaulted", Namespace: "default"},
			Spec:       framev1beta1.FrameJobSpec{Pipeline: "neura-training-dag"},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, job) })

		back := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "defaulted", Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassLow))
		Expect(back.Spec.Priority).To(Equal("medium"))
		Expect(back.Spec.GPUCount).To(BeNumerically("==", 0))
		Expect(back.Spec.Suspended).To(BeFalse())
	})

	DescribeTable("rejects a spec the freeze bounds forbid",
		func(name string, mutate func(*framev1beta1.FrameJobSpec)) {
			job := liveShapedJob(name)
			mutate(&job.Spec)
			err := k8sClient.Create(ctx, job)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, job) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec the schema must reject")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		},
		Entry("a serviceClass outside the three tiers", "reject-unknown-class",
			func(s *framev1beta1.FrameJobSpec) { s.ServiceClass = "URGENT" }),
		Entry("a gpuCount above the 1024 ceiling (T5)", "reject-gpu-ceiling",
			func(s *framev1beta1.FrameJobSpec) { s.GPUCount = 1025 }),
		Entry("a negative gpuCount", "reject-gpu-negative",
			func(s *framev1beta1.FrameJobSpec) { s.GPUCount = -1 }),
		Entry("a pipeline that is not DNS-1123-shaped (T7)", "reject-pipeline-case",
			func(s *framev1beta1.FrameJobSpec) { s.Pipeline = "Neura-Training-DAG" }),
		Entry("an empty pipeline, which the pattern forbids", "reject-pipeline-empty",
			func(s *framev1beta1.FrameJobSpec) { s.Pipeline = "" }),
		Entry("a pipeline longer than 253 characters (T7)", "reject-pipeline-length",
			func(s *framev1beta1.FrameJobSpec) { s.Pipeline = strings.Repeat("a", 254) }),
		Entry("a parameter value longer than 1024 characters (T3)", "reject-param-value",
			func(s *framev1beta1.FrameJobSpec) {
				s.Parameters = map[string]framev1beta1.ParameterValue{
					"dataset": framev1beta1.ParameterValue(strings.Repeat("x", 1025)),
				}
			}),
		Entry("more than 64 parameters (T3)", "reject-param-count",
			func(s *framev1beta1.FrameJobSpec) {
				s.Parameters = map[string]framev1beta1.ParameterValue{}
				for i := range 65 {
					s.Parameters[fmt.Sprintf("p%02d", i)] = "v"
				}
			}),
		Entry("a priority outside the four levels", "reject-priority",
			func(s *framev1beta1.FrameJobSpec) { s.Priority = "urgent" }),
	)

	It("rejects an explicitly empty serviceClass on the wire, but not from a Go client", func() {
		// The empty string is deliberately not an enum member (F4). Proving
		// that needs an unstructured object: the typed FrameJobSpec tags
		// serviceClass omitempty, so a Go client setting it to "" sends no
		// key at all and the CRD default fills in LOW. Absence and
		// explicit-emptiness are indistinguishable through the typed client —
		// which is the very ambiguity the enum removes for everyone else. A
		// hand-written YAML, a kubectl patch or the TypeScript SDK can all
		// still put "" on the wire, and that is what must be rejected.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameJob"))
		raw.SetNamespace("default")
		raw.SetName("reject-empty-class")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"pipeline":     "neura-inference-dag",
			"serviceClass": "",
		}, "spec")).To(Succeed())

		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), `the apiserver accepted serviceClass: ""`)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)

		// The counterpart, so the spec above cannot pass by rejecting
		// everything: the same object with the key absent is accepted and
		// defaulted.
		typed := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "reject-empty-class", Namespace: "default"},
			Spec:       framev1beta1.FrameJobSpec{Pipeline: "neura-inference-dag", ServiceClass: ""},
		}
		Expect(k8sClient.Create(ctx, typed)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, typed) })
		Expect(typed.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassLow))
	})

	It("accepts a spec sitting exactly on each bound", func() {
		// The complement of the table above: a ceiling that rejects the legal
		// maximum is a different bug from one that accepts everything, and
		// only writing both tells them apart.
		job := liveShapedJob("on-the-bound")
		job.Spec.Pipeline = strings.Repeat("a", 253)
		job.Spec.GPUCount = 1024
		job.Spec.Parameters = map[string]framev1beta1.ParameterValue{
			"dataset": framev1beta1.ParameterValue(strings.Repeat("x", 1024)),
		}
		for i := range 63 {
			job.Spec.Parameters[fmt.Sprintf("p%02d", i)] = "v"
		}
		Expect(job.Spec.Parameters).To(HaveLen(64))
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, job) })
	})
})
