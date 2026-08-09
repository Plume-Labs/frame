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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*, not about the SchedulingPolicy
// controller: the enums, the queueWeight ceiling and the inherited
// object-level CEL rule are enforced by the apiserver from the CRD, so nothing
// but a real apiserver can show them working. They run against the CRDs as
// kustomize renders them (renderedCRDPath), which is the artefact the cluster
// installs.
//
// Every rejection is paired with an acceptance. In particular the two patterns
// deliberately admit the empty string, so each is asserted in both directions:
// a pattern that rejects "" would be a silent regression for a user clearing
// the field in the SDK's create form.
var _ = Describe("SchedulingPolicy v1beta1 schema", func() {
	// liveShapedPolicy is modelled field-for-field on neura-default, the one
	// SchedulingPolicy stored on the test cluster: volcano, queue and
	// priorityClass both neura-high, preemption true, priorityValue 100,
	// queueWeight 100. If v1beta1 cannot hold this, conversion has nowhere to
	// put the object that exists.
	liveShapedPolicy := func(name string) *framev1beta1.SchedulingPolicy {
		return &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:     "volcano",
				QueueName:     "neura-high",
				PriorityClass: "neura-high",
				Preemption:    true,
				PriorityValue: new(int32(100)),
				QueueWeight:   new(int32(100)),
			},
		}
	}

	It("holds the stored object's shape without loss, status included", func() {
		policy := liveShapedPolicy("live-shaped-policy")
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })

		key := types.NamespacedName{Name: "live-shaped-policy", Namespace: "default"}

		policy.Status.ObservedGeneration = 1
		policy.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Applied",
			Message:            "scheduler=volcano priorityClass=neura-high queue=neura-high",
			ObservedGeneration: 1,
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, policy)).To(Succeed())

		back := &framev1beta1.SchedulingPolicy{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.Scheduler).To(Equal("volcano"))
		Expect(back.Spec.QueueName).To(Equal("neura-high"))
		Expect(back.Spec.PriorityClass).To(Equal("neura-high"))
		Expect(back.Spec.Preemption).To(BeTrue())
		Expect(back.Spec.PriorityValue).To(HaveValue(BeNumerically("==", 100)))
		Expect(back.Spec.QueueWeight).To(HaveValue(BeNumerically("==", 100)))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 1))
		Expect(back.Status.Conditions).To(HaveLen(1))
		Expect(back.Status.Conditions[0].Reason).To(Equal("Applied"))
	})

	DescribeTable("rejects a spec the freeze forbids",
		func(name string, mutate func(*framev1beta1.SchedulingPolicySpec)) {
			policy := liveShapedPolicy(name)
			mutate(&policy.Spec)
			err := k8sClient.Create(ctx, policy)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec the schema must reject")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		},
		// T5: the ceiling is new. v1alpha1 accepted every one of these.
		Entry("a queueWeight above 1000000 (T5)", "reject-weight-ceiling",
			func(s *framev1beta1.SchedulingPolicySpec) { s.QueueWeight = new(int32(1000001)) }),
		Entry("a queueWeight at the int32 maximum (T5)", "reject-weight-int32max",
			func(s *framev1beta1.SchedulingPolicySpec) { s.QueueWeight = new(int32(2147483647)) }),
		Entry("a queueWeight of zero, below the inherited floor", "reject-weight-zero",
			func(s *framev1beta1.SchedulingPolicySpec) { s.QueueWeight = new(int32(0)) }),
		// priorityValue was already bounded; the bound is asserted so a later
		// task cannot drop it while adding queueWeight's.
		Entry("a priorityValue above 1000000000", "reject-priority-ceiling",
			func(s *framev1beta1.SchedulingPolicySpec) { s.PriorityValue = new(int32(1000000001)) }),
		Entry("a scheduler outside the enum", "reject-scheduler",
			func(s *framev1beta1.SchedulingPolicySpec) { s.Scheduler = "kube-scheduler" }),
		// Note: an empty scheduler through the typed client is rejected by the
		// *enum*, not by Required — the json tag has no omitempty, so ""
		// reaches the wire as a value rather than as an omission. Required is
		// asserted separately below, where the key is absent entirely.
		Entry("an empty scheduler, which is not an enum member", "reject-scheduler-empty",
			func(s *framev1beta1.SchedulingPolicySpec) { s.Scheduler = "" }),
		Entry("a queueName that is not a Kubernetes object name", "reject-queue-chars",
			func(s *framev1beta1.SchedulingPolicySpec) { s.QueueName = "Neura_High" }),
		Entry("a queueName longer than 253 characters", "reject-queue-length",
			func(s *framev1beta1.SchedulingPolicySpec) { s.QueueName = strings.Repeat("q", 254) }),
		Entry("a priorityClass that is not a Kubernetes object name", "reject-class-chars",
			func(s *framev1beta1.SchedulingPolicySpec) { s.PriorityClass = "neura high" }),
		Entry("a priorityClass longer than 253 characters", "reject-class-length",
			func(s *framev1beta1.SchedulingPolicySpec) { s.PriorityClass = strings.Repeat("p", 254) }),
		// The inherited object-level rule, which the freeze makes permanent.
		// Pinned so a later task cannot loosen or drop it unnoticed.
		Entry("preemption without a priorityClass", "reject-preemption-no-class",
			func(s *framev1beta1.SchedulingPolicySpec) { s.PriorityClass = "" }),
	)

	It("rejects a spec with no scheduler key at all, which is what Required buys", func() {
		// Nothing in the typed table above actually tests Required: the json
		// tag carries no omitempty, so a Go client sending "" sends the key,
		// and the enum rejects it first. Deleting the Required marker leaves
		// every one of those entries passing. Only an object that omits the
		// key reaches the required-fields check.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("SchedulingPolicy"))
		raw.SetNamespace("default")
		raw.SetName("reject-absent-scheduler")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"queueName": "neura-high",
		}, "spec")).To(Succeed())

		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted a policy with no scheduler")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring("scheduler"),
			"the rejection must name scheduler, not some other rule")

		// The counterpart: the very same spec with scheduler supplied is
		// accepted, so the rejection above is about the missing key alone.
		ok := &unstructured.Unstructured{}
		ok.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("SchedulingPolicy"))
		ok.SetNamespace("default")
		ok.SetName("accept-present-scheduler")
		Expect(unstructured.SetNestedMap(ok.Object, map[string]any{
			"scheduler": "volcano",
			"queueName": "neura-high",
		}, "spec")).To(Succeed())
		Expect(k8sClient.Create(ctx, ok)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ok) })
	})

	It("rejects preemption with an explicitly empty priorityClass", func() {
		// The rule reads `size(self.priorityClass) > 0`, not just `has(...)`.
		// The typed spec tags priorityClass omitempty, so a Go client setting
		// it to "" sends no key — only an unstructured object can show the
		// size() clause doing work, which is the form a kubectl patch or the
		// TypeScript SDK's create form would send.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("SchedulingPolicy"))
		raw.SetNamespace("default")
		raw.SetName("reject-preemption-empty-class")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"scheduler":     "volcano",
			"preemption":    true,
			"priorityClass": "",
		}, "spec")).To(Succeed())

		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), `the apiserver accepted preemption: true with priorityClass: ""`)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
	})

	It("accepts a spec sitting exactly on each bound", func() {
		// The complement of the table above: a ceiling that rejects the legal
		// maximum is a different bug from one that accepts everything.
		policy := liveShapedPolicy("on-the-bound-policy")
		policy.Spec.QueueWeight = new(int32(1000000))
		policy.Spec.PriorityValue = new(int32(1000000000))
		policy.Spec.QueueName = strings.Repeat("q", 253)
		policy.Spec.PriorityClass = strings.Repeat("p", 253)
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })

		// The other end of each numeric range, so the ceilings above cannot
		// pass by accepting everything in one direction only.
		floors := liveShapedPolicy("on-the-floor-policy")
		floors.Spec.QueueWeight = new(int32(1))
		floors.Spec.PriorityValue = new(int32(-2147483648))
		Expect(k8sClient.Create(ctx, floors)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, floors) })
	})

	It("accepts an explicitly empty queueName and priorityClass, which the patterns deliberately admit", func() {
		// Documented decision, not an accident: the controller branches on ""
		// to skip queue reconciliation, and the SDK's create form sends both
		// fields unconditionally, so a user clearing one must still be able to
		// save. Asserted through an unstructured object because the typed spec
		// would omit the keys entirely and prove nothing about the pattern.
		//
		// preemption is false here — with it true, the object-level rule would
		// reject the empty priorityClass for an unrelated reason.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("SchedulingPolicy"))
		raw.SetNamespace("default")
		raw.SetName("accept-empty-strings")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"scheduler":     "default",
			"queueName":     "",
			"priorityClass": "",
			"preemption":    false,
		}, "spec")).To(Succeed())

		Expect(k8sClient.Create(ctx, raw)).To(Succeed(), `the apiserver rejected queueName: "" / priorityClass: ""`)
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
	})

	It("accepts a policy without preemption and without a priorityClass", func() {
		// The preemption rejection above must fail for the reason stated and
		// not because priorityClass is effectively mandatory. This also pins
		// the preemption default: absent means false, so the rule is
		// vacuously satisfied.
		policy := liveShapedPolicy("no-preemption-policy")
		policy.Spec.Preemption = false
		policy.Spec.PriorityClass = ""
		policy.Spec.PriorityValue = nil
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })

		back := &framev1beta1.SchedulingPolicy{}
		key := types.NamespacedName{Name: "no-preemption-policy", Namespace: "default"}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.Preemption).To(BeFalse())
	})

	DescribeTable("accepts every scheduler in the enum",
		func(name, scheduler string) {
			policy := liveShapedPolicy(name)
			policy.Spec.Scheduler = scheduler
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, policy) })
		},
		Entry("volcano", "accept-volcano", "volcano"),
		Entry("yunikorn", "accept-yunikorn", "yunikorn"),
		Entry("default", "accept-default-scheduler", "default"),
	)
})
