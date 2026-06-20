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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

var _ = Describe("FrameResourceQuota Controller", func() {
	const name = "test-frq"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	frq := &framev1alpha1.FrameResourceQuota{}
	cpu := resource.MustParse("100")

	BeforeEach(func() {
		*frq = framev1alpha1.FrameResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1alpha1.FrameResourceQuotaSpec{
				ServiceClass: "HIGH",
				MaxGPUs:      8,
				MaxCPU:       &cpu,
				MaxJobs:      10,
			},
		}
		Expect(k8sClient.Create(ctx, frq)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1alpha1.FrameResourceQuota{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.FrameResourceQuota{}))
		}, "5s").Should(BeTrue())
	})

	r := func() *FrameResourceQuotaReconciler {
		return &FrameResourceQuotaReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, frq)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(frq, frameResourceQuotaFinalizer)).To(BeTrue())
	})

	It("sets Ready=True with 0 matching namespaces", func() {
		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, frq)).To(Succeed())
		var readyCond *metav1.Condition
		for i := range frq.Status.Conditions {
			if frq.Status.Conditions[i].Type == "Ready" {
				readyCond = &frq.Status.Conditions[i]
			}
		}
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(readyCond.Message).To(ContainSubstring("0 namespaces"))
	})

	It("creates ResourceQuota in a labeled namespace", func() {
		labeledNS := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "high-workload",
				Labels: map[string]string{"frame.plume-labs.io/service-class": "HIGH"},
			},
		}
		Expect(k8sClient.Create(ctx, labeledNS)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, labeledNS) })

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		quota := &corev1.ResourceQuota{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "frame-high", Namespace: "high-workload",
		}, quota)).To(Succeed())
		Expect(quota.Spec.Hard[corev1.ResourceName("requests.nvidia.com/gpu")]).
			To(Equal(resource.MustParse("8")))
	})
})
