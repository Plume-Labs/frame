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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
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
				Name:         name,
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
		return &FrameJobReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, job)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(job, frameJobFinalizer)).To(BeTrue())
	})

	It("attempts ArgoWorkflow creation on second reconcile", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		// Argo CRD not installed in envtest — expect Create error, not panic.
		_, _ = r().Reconcile(ctx, req)

		// Object still exists and retains finalizer.
		Expect(k8sClient.Get(ctx, key, job)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(job, frameJobFinalizer)).To(BeTrue())
	})
})
