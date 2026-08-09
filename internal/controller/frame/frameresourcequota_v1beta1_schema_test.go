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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*, not about the FrameResourceQuota
// controller: the enum, the maxGPUs ceiling and the inherited object-level CEL
// rule are enforced by the apiserver from the CRD, so nothing but a real
// apiserver can show them working. They run against the CRDs as kustomize
// renders them (renderedCRDPath), which is the artefact the cluster installs.
//
// Every rejection is paired with an acceptance. A ceiling that rejects the
// legal maximum is a different bug from one that accepts everything, and a
// suite that only ever asserts failure cannot tell them apart.
var _ = Describe("FrameResourceQuota v1beta1 schema", func() {
	// notAServiceClass is a tier no version of the enum has ever admitted. The
	// FrameJob and FrameNode suites use the same string for the same purpose.
	const notAServiceClass framev1beta1.ServiceClass = "URGENT"

	mustQuantity := func(s string) *resource.Quantity {
		q := resource.MustParse(s)
		return &q
	}

	// liveShapedQuota is modelled field-for-field on quota-high, one of the
	// three FrameResourceQuotas stored on the test cluster (HIGH, maxGPUs 0,
	// maxCPU 8, maxMemory 16Gi, maxJobs 20). If v1beta1 cannot hold this,
	// conversion has nowhere to put the objects that exist.
	liveShapedQuota := func(name string) *framev1beta1.FrameResourceQuota {
		return &framev1beta1.FrameResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameResourceQuotaSpec{
				ServiceClass: framev1beta1.ServiceClassHigh,
				MaxGPUs:      0,
				MaxCPU:       mustQuantity("8"),
				MaxMemory:    mustQuantity("16Gi"),
				MaxJobs:      20,
			},
		}
	}

	It("holds a stored object's shape without loss, status included", func() {
		quota := liveShapedQuota("live-shaped-quota")
		Expect(k8sClient.Create(ctx, quota)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, quota) })

		key := types.NamespacedName{Name: "live-shaped-quota", Namespace: "default"}

		// The stored objects sit at generation 3 with Ready at
		// observedGeneration 2 — a lagging observedGeneration is the state
		// conversion has to carry, so it is the state written here rather
		// than a tidy matching pair.
		quota.Status.ObservedGeneration = 2
		quota.Status.Namespaces = 0
		quota.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Reconciled",
			Message:            "Applied to 0 namespaces",
			ObservedGeneration: 2,
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, quota)).To(Succeed())

		back := &framev1beta1.FrameResourceQuota{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassHigh))
		Expect(back.Spec.MaxCPU.String()).To(Equal("8"))
		Expect(back.Spec.MaxMemory.String()).To(Equal("16Gi"))
		Expect(back.Spec.MaxJobs).To(BeNumerically("==", 20))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 2))
		Expect(back.Status.Conditions).To(HaveLen(1))
		Expect(back.Status.Conditions[0].ObservedGeneration).To(BeNumerically("==", 2))
	})

	It("accepts the three stored specs exactly as they sit on the wire", func() {
		// The typed fixture above tags maxGPUs omitempty, so a Go client
		// setting it to 0 sends no key at all — but all three stored objects
		// were created by `kubectl apply` and carry an explicit `maxGPUs: 0`.
		// That is the value the object-level rule actually sees on conversion,
		// so it is asserted through an unstructured client, which is the only
		// way to put the literal zero on the wire.
		for name, stored := range map[string]map[string]any{
			"stored-high":   {"serviceClass": "HIGH", "maxGPUs": int64(0), "maxCPU": "8", "maxMemory": "16Gi", "maxJobs": int64(20)},
			"stored-medium": {"serviceClass": "MEDIUM", "maxGPUs": int64(0), "maxCPU": "4", "maxMemory": "8Gi", "maxJobs": int64(10)},
			"stored-low":    {"serviceClass": "LOW", "maxGPUs": int64(0), "maxCPU": "2", "maxMemory": "4Gi", "maxJobs": int64(5)},
		} {
			raw := &unstructured.Unstructured{}
			raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameResourceQuota"))
			raw.SetNamespace("default")
			raw.SetName(name)
			Expect(unstructured.SetNestedMap(raw.Object, stored, "spec")).To(Succeed())
			Expect(k8sClient.Create(ctx, raw)).To(Succeed(), "v1beta1 rejected the stored spec %q", name)
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
	})

	It("serializes status.namespaces at zero but omits an empty status.used", func() {
		// The asymmetry is deliberate and load-bearing on every object that
		// exists: all three stored quotas currently match zero namespaces.
		// namespaces is a count, so 0 is a measurement distinct from "never
		// reconciled" and must reach the wire; used is a set, so an empty one
		// carries nothing and is omitted. Only an unstructured read can tell
		// "absent" from "present and zero" — a typed Get gives 0 for both.
		quota := liveShapedQuota("zero-namespaces")
		Expect(k8sClient.Create(ctx, quota)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, quota) })

		quota.Status.ObservedGeneration = 1
		quota.Status.Namespaces = 0
		quota.Status.Used = nil
		Expect(k8sClient.Status().Update(ctx, quota)).To(Succeed())

		key := types.NamespacedName{Name: "zero-namespaces", Namespace: "default"}
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameResourceQuota"))
		Expect(k8sClient.Get(ctx, key, raw)).To(Succeed())

		namespaces, found, err := unstructured.NestedInt64(raw.Object, "status", "namespaces")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "status.namespaces must be serialized at 0, not omitted")
		Expect(namespaces).To(BeNumerically("==", 0))

		// Belt and braces rather than a proof of the omitempty: dropping the
		// marker does not fail this, because a nil map marshals to null and
		// the apiserver prunes nulls anyway. What is pinned here is the
		// observable contract — an unset used reads back absent — which is
		// what a client distinguishing "no usage reported" from "usage of
		// zero" depends on.
		_, found, err = unstructured.NestedMap(raw.Object, "status", "used")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "an empty status.used must be absent, not written as {} or null")

		// The counterpart, so the omission above cannot pass because the
		// status subresource silently rejected the whole update: a populated
		// used does reach the wire.
		Expect(k8sClient.Get(ctx, key, quota)).To(Succeed())
		quota.Status.Used = corev1.ResourceList{
			"limits.cpu":                          resource.MustParse("2"),
			"count/framejobs.frame.plume-labs.io": resource.MustParse("3"),
		}
		quota.Status.Namespaces = 1
		Expect(k8sClient.Status().Update(ctx, quota)).To(Succeed())

		back := &framev1beta1.FrameResourceQuota{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Status.Used).To(HaveKey(corev1.ResourceName("limits.cpu")))
		Expect(back.Status.Used).To(HaveKey(corev1.ResourceName("count/framejobs.frame.plume-labs.io")))
		Expect(back.Status.Namespaces).To(BeNumerically("==", 1))
	})

	DescribeTable("rejects a spec the freeze forbids",
		func(name string, mutate func(*framev1beta1.FrameResourceQuotaSpec)) {
			quota := liveShapedQuota(name)
			mutate(&quota.Spec)
			err := k8sClient.Create(ctx, quota)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, quota) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec the schema must reject")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		},
		// T5: the ceiling is new. v1alpha1 accepted every one of these.
		Entry("a maxGPUs above 1024 (T5)", "reject-gpus-ceiling",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxGPUs = 1025 }),
		Entry("a wildly out-of-range maxGPUs (T5)", "reject-gpus-typo",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxGPUs = 100000 }),
		Entry("a negative maxGPUs", "reject-gpus-negative",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxGPUs = -1 }),
		Entry("a negative maxJobs", "reject-jobs-negative",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxJobs = -1 }),
		// F4: the shared enum, carrying its members on the ServiceClass type.
		Entry("a serviceClass outside the three tiers (F4)", "reject-unknown-class",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.ServiceClass = notAServiceClass }),
		Entry("a lowercase serviceClass (F4)", "reject-lowercase-class",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.ServiceClass = "high" }),
		// Note: an empty ServiceClass through the typed client is rejected by
		// the *enum*, not by Required — the json tag has no omitempty, so ""
		// reaches the wire as a value rather than as an omission. Required is
		// asserted separately below, where the key is absent entirely.
		Entry("an empty serviceClass, which is not an enum member (F4)", "reject-empty-class-typed",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.ServiceClass = "" }),
		// The inherited object-level rule, which the freeze makes permanent.
		// Pinned so a later task cannot loosen or drop it unnoticed.
		Entry("a quota that caps nothing at all", "reject-no-limits",
			func(s *framev1beta1.FrameResourceQuotaSpec) {
				*s = framev1beta1.FrameResourceQuotaSpec{ServiceClass: framev1beta1.ServiceClassLow}
			}),
	)

	It("rejects an explicitly zero-valued quota, so the rule is not merely a presence check", func() {
		// The rule reads `self.maxGPUs > 0`, not `has(self.maxGPUs)`. Through
		// the typed client that distinction is invisible — omitempty means a
		// zero never reaches the wire — so it takes an unstructured object to
		// show that a quota which explicitly caps everything at zero is
		// rejected rather than accepted as "four fields present".
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameResourceQuota"))
		raw.SetNamespace("default")
		raw.SetName("reject-explicit-zeroes")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"serviceClass": "LOW",
			"maxGPUs":      int64(0),
			"maxJobs":      int64(0),
		}, "spec")).To(Succeed())

		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted maxGPUs: 0 with maxJobs: 0")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
	})

	It("rejects a spec with no serviceClass key at all, which is what Required buys", func() {
		// F4 keeps serviceClass Required for this kind alone. Nothing in the
		// typed table above actually tests that: the json tag carries no
		// omitempty, so a Go client sending "" sends the key, and the enum
		// rejects it first. Deleting the Required marker leaves every one of
		// those entries passing. Only an object that omits the key reaches
		// the required-fields check — and it has to satisfy the object-level
		// rule too, or it would be rejected for that instead.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameResourceQuota"))
		raw.SetNamespace("default")
		raw.SetName("reject-absent-class")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"maxJobs": int64(5),
		}, "spec")).To(Succeed())

		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted a quota with no serviceClass")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring("serviceClass"),
			"the rejection must name serviceClass, not some other rule")

		// The counterpart: the very same spec with serviceClass supplied is
		// accepted, so the rejection above is about the missing key and not
		// about maxJobs or the object-level rule.
		ok := &unstructured.Unstructured{}
		ok.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameResourceQuota"))
		ok.SetNamespace("default")
		ok.SetName("accept-present-class")
		Expect(unstructured.SetNestedMap(ok.Object, map[string]any{
			"serviceClass": "LOW",
			"maxJobs":      int64(5),
		}, "spec")).To(Succeed())
		Expect(k8sClient.Create(ctx, ok)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ok) })
	})

	It("rejects an explicitly empty serviceClass on the wire", func() {
		// The empty string is deliberately not an enum member (F4). Proving
		// its absence needs an unstructured object: the typed spec would send
		// "" and be rejected as a missing required field instead, which is a
		// different rejection for a different reason.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameResourceQuota"))
		raw.SetNamespace("default")
		raw.SetName("reject-empty-class")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"serviceClass": "",
			"maxJobs":      int64(5),
		}, "spec")).To(Succeed())

		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), `the apiserver accepted serviceClass: ""`)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
	})

	It("accepts a spec sitting exactly on each bound", func() {
		// The complement of the table above: a ceiling that rejects the legal
		// maximum is a different bug from one that accepts everything.
		quota := liveShapedQuota("on-the-bound-quota")
		quota.Spec.MaxGPUs = 1024
		Expect(k8sClient.Create(ctx, quota)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, quota) })
	})

	DescribeTable("accepts a quota that sets any single limit, so the object-level rule bans only the empty one",
		func(name string, mutate func(*framev1beta1.FrameResourceQuotaSpec)) {
			quota := liveShapedQuota(name)
			quota.Spec = framev1beta1.FrameResourceQuotaSpec{ServiceClass: framev1beta1.ServiceClassMedium}
			mutate(&quota.Spec)
			Expect(k8sClient.Create(ctx, quota)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, quota) })
		},
		Entry("maxGPUs alone", "accept-gpus-only",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxGPUs = 1 }),
		Entry("maxCPU alone", "accept-cpu-only",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxCPU = mustQuantity("500m") }),
		Entry("maxMemory alone", "accept-memory-only",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxMemory = mustQuantity("1Gi") }),
		Entry("maxJobs alone", "accept-jobs-only",
			func(s *framev1beta1.FrameResourceQuotaSpec) { s.MaxJobs = 1 }),
	)
})
