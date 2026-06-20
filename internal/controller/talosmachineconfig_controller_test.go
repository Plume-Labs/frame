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

var _ = Describe("TalosMachineConfig Controller", func() {
	const name = "test-tmc"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	tmc := &framev1alpha1.TalosMachineConfig{}

	BeforeEach(func() {
		*tmc = framev1alpha1.TalosMachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1alpha1.TalosMachineConfigSpec{
				NodeName:      "worker-1",
				TalosEndpoint: "10.0.0.1:50000",
				TalosSecretRef: corev1.SecretReference{
					Name:      "talos-creds",
					Namespace: ns,
				},
				ConfigPatch: "machine:\n  network:\n    hostname: worker-1\n",
			},
		}
		Expect(k8sClient.Create(ctx, tmc)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1alpha1.TalosMachineConfig{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.TalosMachineConfig{}))
		}, "5s").Should(BeTrue())
	})

	r := func() *TalosMachineConfigReconciler {
		return &TalosMachineConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, tmc)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(tmc, talosMachineConfigFinalizer)).To(BeTrue())
	})

	It("sets PatchResolved condition when patch is inline", func() {
		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, tmc)).To(Succeed())
		var cond *metav1.Condition
		for i := range tmc.Status.Conditions {
			if tmc.Status.Conditions[i].Type == "Ready" {
				cond = &tmc.Status.Conditions[i]
			}
		}
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("PatchResolved"))
	})
})
