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
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

var _ = Describe("FrameNode Controller", func() {
	const name = "test-framenode"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	fn := &framev1alpha1.FrameNode{}

	BeforeEach(func() {
		*fn = framev1alpha1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1alpha1.FrameNodeSpec{
				IP: "10.0.0.1", Role: "worker", Disk: "/dev/nvme0n1",
				Rack: "rack1", Zone: "zone-a", ServiceClass: "HIGH",
				Network: framev1alpha1.NetworkSpec{
					Address: "10.0.0.1/24",
					Gateway: "10.0.0.1",
					DNS:     []string{"1.1.1.1"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, fn)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1alpha1.FrameNode{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.FrameNode{}))
		}, "5s").Should(BeTrue())
	})

	r := func() *FrameNodeReconciler {
		return &FrameNodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, fn)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(fn, frameNodeFinalizer)).To(BeTrue())
	})

	It("sets phase=Provisioning when k8s Node does not exist", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, fn)).To(Succeed())
		Expect(fn.Status.Phase).To(Equal("Provisioning"))
	})

	It("sets Ready=False condition when Provisioning", func() {
		_, _ = r().Reconcile(ctx, req)
		_, _ = r().Reconcile(ctx, req)

		Expect(k8sClient.Get(ctx, key, fn)).To(Succeed())
		var readyCond *metav1.Condition
		for i := range fn.Status.Conditions {
			if fn.Status.Conditions[i].Type == "Ready" {
				readyCond = &fn.Status.Conditions[i]
			}
		}
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal("Provisioning"))
	})
})
