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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

var _ = Describe("SchedulingPolicy Controller", func() {
	const name = "test-policy"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	sp := &framev1alpha1.SchedulingPolicy{}

	BeforeEach(func() {
		*sp = framev1alpha1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1alpha1.SchedulingPolicySpec{
				Scheduler:     "default",
				QueueName:     "batch",
				PriorityClass: "neura-low",
			},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1alpha1.SchedulingPolicy{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.SchedulingPolicy{}))
		}, "5s").Should(BeTrue())
		_ = k8sClient.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "frame-policy-" + name, Namespace: "kube-system"},
		})
	})

	r := func() *SchedulingPolicyReconciler {
		return &SchedulingPolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeTrue())
	})

	It("creates a ConfigMap in kube-system", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "frame-policy-" + name, Namespace: "kube-system",
		}, cm)).To(Succeed())
		Expect(cm.Data["scheduler"]).To(Equal("default"))
		Expect(cm.Data["queue"]).To(Equal("batch"))
	})

	It("sets Ready=True condition after reconcile", func() {
		_, _ = r().Reconcile(ctx, req)
		_, _ = r().Reconcile(ctx, req)

		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		var readyCond *metav1.Condition
		for i := range sp.Status.Conditions {
			if sp.Status.Conditions[i].Type == "Ready" {
				readyCond = &sp.Status.Conditions[i]
			}
		}
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
	})
})
