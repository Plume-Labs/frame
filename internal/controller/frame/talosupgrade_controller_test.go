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
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

var _ = Describe("TalosUpgrade Controller", func() {
	const name = "test-upgrade"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	tu := &framev1alpha1.TalosUpgrade{}

	BeforeEach(func() {
		*tu = framev1alpha1.TalosUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1alpha1.TalosUpgradeSpec{
				NodeName:      "worker-1",
				TalosEndpoint: "10.0.0.1:50000",
				TalosSecretRef: corev1.SecretReference{
					Name:      "talos-upgrade-creds",
					Namespace: ns,
				},
				Image:        "ghcr.io/siderolabs/installer:v1.8.0",
				PreserveData: true,
			},
		}
		Expect(k8sClient.Create(ctx, tu)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1alpha1.TalosUpgrade{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.TalosUpgrade{}))
		}, "5s").Should(BeTrue())
	})

	r := func() *TalosUpgradeReconciler {
		return &TalosUpgradeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, tu)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(tu, talosUpgradeFinalizer)).To(BeTrue())
	})

	It("sets ClientBuildFailed condition when talos-creds secret does not exist", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, tu)).To(Succeed())
		cond := findCondition(tu.Status.Conditions, "Ready")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("ClientBuildFailed"))
	})

	It("sets failure condition and does not return error when Talos unreachable", func() {
		// Secret exists with correct key names but fake (non-PEM) data.
		// buildTalosClient will fail parsing the TLS credentials → ClientBuildFailed.
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "talos-upgrade-creds", Namespace: ns},
			Data: map[string][]byte{
				"ca":  []byte("fake-ca"),
				"crt": []byte("fake-crt"),
				"key": []byte("fake-key"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })

		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred()) // must not propagate Talos errors

		Expect(k8sClient.Get(ctx, key, tu)).To(Succeed())
		cond := findCondition(tu.Status.Conditions, "Ready")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		// Either ClientBuildFailed (TLS parse error) or UpgradeFailed (dial error).
		Expect(cond.Reason).To(BeElementOf("ClientBuildFailed", "UpgradeFailed"))
	})

	It("does not re-submit upgrade after condition is already recorded for same generation", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "talos-upgrade-creds", Namespace: ns},
			Data: map[string][]byte{
				"ca":  []byte("fake-ca"),
				"crt": []byte("fake-crt"),
				"key": []byte("fake-key"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })

		_, _ = r().Reconcile(ctx, req)
		_, _ = r().Reconcile(ctx, req) // sets failure condition for gen 1

		// Manually set condition to "UpgradeRequested" to simulate success.
		Expect(k8sClient.Get(ctx, key, tu)).To(Succeed())
		p := client.MergeFrom(tu.DeepCopy())
		setCondition(&tu.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "UpgradeRequested",
			ObservedGeneration: tu.Generation,
		})
		Expect(k8sClient.Status().Patch(ctx, tu, p)).To(Succeed())

		// Third reconcile should be a no-op (generation unchanged).
		result, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		// Condition must still reflect the manually-set value.
		Expect(k8sClient.Get(ctx, key, tu)).To(Succeed())
		cond := findCondition(tu.Status.Conditions, "Ready")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("UpgradeRequested"))
	})
})
