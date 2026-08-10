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
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

var _ = Describe("SchedulingPolicy Controller", func() {
	const name = "test-policy"
	const ns = "default"
	const pcName = "frame-test-pc"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	sp := &framev1beta1.SchedulingPolicy{}

	prio := int32(500)

	BeforeEach(func() {
		*sp = framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:     "default",
				PriorityClass: pcName,
				PriorityValue: &prio,
			},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1beta1.SchedulingPolicy{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1beta1.SchedulingPolicy{}))
		}, "5s").Should(BeTrue())
		_ = k8sClient.Delete(ctx, &schedulingv1.PriorityClass{
			ObjectMeta: metav1.ObjectMeta{Name: pcName},
		})
	})

	r := func() *SchedulingPolicyReconciler {
		return &SchedulingPolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeTrue())
	})

	It("creates a PriorityClass with the configured value", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		pc := &schedulingv1.PriorityClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, pc)).To(Succeed())
		Expect(pc.Value).To(Equal(int32(500)))
		Expect(pc.Labels["frame.plume-labs.io/policy-namespace"]).To(Equal(ns))
		Expect(pc.Labels["frame.plume-labs.io/policy-name"]).To(Equal(name))

		pp := corev1.PreemptNever
		Expect(pc.PreemptionPolicy).To(Equal(&pp))
	})

	It("sets PreemptLowerPriority on PriorityClass when Preemption=true", func() {
		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		sp.Spec.Preemption = true
		Expect(k8sClient.Update(ctx, sp)).To(Succeed())

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		pc := &schedulingv1.PriorityClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, pc)).To(Succeed())
		pp := corev1.PreemptLowerPriority
		Expect(pc.PreemptionPolicy).To(Equal(&pp))
	})

	It("sets Ready=True condition after successful reconcile", func() {
		_, _ = r().Reconcile(ctx, req)
		_, _ = r().Reconcile(ctx, req)

		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		cond := findCondition(sp.Status.Conditions)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("Applied"))
	})

	It("degrades Ready condition when queue CRD is missing (volcano scheduler)", func() {
		// Volcano Queue CRD is not installed in envtest — controller must degrade gracefully.
		weight := int32(2)
		spV := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "volcano-policy", Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:   "volcano",
				QueueName:   "hpc",
				QueueWeight: &weight,
			},
		}
		Expect(k8sClient.Create(ctx, spV)).To(Succeed())
		defer func() {
			fresh := &framev1beta1.SchedulingPolicy{}
			vKey := types.NamespacedName{Name: "volcano-policy", Namespace: ns}
			if err := k8sClient.Get(ctx, vKey, fresh); err == nil {
				fresh.Finalizers = nil
				_ = k8sClient.Update(ctx, fresh)
				_ = k8sClient.Delete(ctx, fresh)
			}
		}()

		vReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: "volcano-policy", Namespace: ns}}
		_, _ = r().Reconcile(ctx, vReq) // add finalizer
		_, err := r().Reconcile(ctx, vReq)
		Expect(err).NotTo(HaveOccurred()) // must not propagate queue error

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "volcano-policy", Namespace: ns}, spV)).To(Succeed())
		cond := findCondition(spV.Status.Conditions)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("ReconcileError"))
	})
})

// findCondition returns the Ready condition — the only condition type any
// caller looks up — or nil if it hasn't been set yet.
func findCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionTypeReady {
			return &conditions[i]
		}
	}
	return nil
}
