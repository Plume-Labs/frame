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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
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
				TalosSecretRef: framev1alpha1.TalosSecretReference{
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
		return &TalosMachineConfigReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, tmc)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(tmc, talosMachineConfigFinalizer)).To(BeTrue())
	})

	It("sets ClientBuildFailed condition when talos-creds secret is absent", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, tmc)).To(Succeed())
		cond := findCondition(tmc.Status.Conditions)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("ClientBuildFailed"))
	})

	It("sets PatchResolveFailed condition when configPatchRef ConfigMap is missing", func() {
		tmcCM := &framev1alpha1.TalosMachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "tmc-cm", Namespace: ns},
			Spec: framev1alpha1.TalosMachineConfigSpec{
				NodeName:       "worker-2",
				TalosEndpoint:  "10.0.0.2:50000",
				TalosSecretRef: framev1alpha1.TalosSecretReference{Name: "talos-creds", Namespace: ns},
				ConfigPatchRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "nonexistent-cm"},
					Key:                  "patch.yaml",
				},
			},
		}
		Expect(k8sClient.Create(ctx, tmcCM)).To(Succeed())
		cmKey := types.NamespacedName{Name: "tmc-cm", Namespace: ns}
		cmReq := reconcile.Request{NamespacedName: cmKey}
		defer func() {
			fresh := &framev1alpha1.TalosMachineConfig{}
			if err := k8sClient.Get(ctx, cmKey, fresh); err == nil {
				fresh.Finalizers = nil
				_ = k8sClient.Update(ctx, fresh)
				_ = k8sClient.Delete(ctx, fresh)
			}
		}()

		_, _ = r().Reconcile(ctx, cmReq) // add finalizer
		_, err := r().Reconcile(ctx, cmReq)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, cmKey, tmcCM)).To(Succeed())
		cond := findCondition(tmcCM.Status.Conditions)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("PatchResolveFailed"))
	})

	It("does not re-apply once Applied is recorded for the same generation", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer

		// Manually record a successful Applied condition, as if resolvePatch
		// and ApplyConfiguration had already succeeded for this generation.
		Expect(k8sClient.Get(ctx, key, tmc)).To(Succeed())
		p := client.MergeFrom(tmc.DeepCopy())
		meta.SetStatusCondition(&tmc.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "Applied",
			Message:            "Config patch applied to worker-1 via 10.0.0.1:50000",
			ObservedGeneration: tmc.Generation,
		})
		Expect(k8sClient.Status().Patch(ctx, tmc, p)).To(Succeed())

		// Without the ObservedGeneration guard this reconcile would call
		// resolvePatch/buildTalosClient again, fail on the absent talos-creds
		// secret, and overwrite Applied with ClientBuildFailed — re-running
		// ApplyConfiguration against a live machine for no reason is exactly
		// what the guard exists to prevent.
		result, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		Expect(k8sClient.Get(ctx, key, tmc)).To(Succeed())
		cond := findCondition(tmc.Status.Conditions)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("Applied"))
	})
})
