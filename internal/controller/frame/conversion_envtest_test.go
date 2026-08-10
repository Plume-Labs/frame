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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// What a unit test of ConvertTo/ConvertFrom cannot prove: that the apiserver
// can *call* them. The CA, the service coordinate, the path, the
// conversionReviewVersions and the manager actually serving /convert are all
// manifest plumbing, and none of it is exercised by a Go test. These specs
// go through a real apiserver, against the CRDs as kustomize renders them
// (see renderedCRDPath), with envtest's WebhookInstallOptions having
// rewritten each clientConfig to the locally-served webhook (F14 point 3).
//
// They also prove the thing point 4 is about, in miniature: an object
// *written* as v1alpha1 and *read back* as v1beta1 goes through ConvertTo on
// the way in, because v1beta1 is the storage version. Task 21's e2e spec does
// the same against objects that were stored before the webhook existed.
var _ = Describe("Conversion", func() {
	ctx := context.Background()

	It("stores a v1alpha1 FrameJob at v1beta1 and drops spec.namespace", func() {
		alpha := &framev1alpha1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-job", Namespace: "default"},
			Spec: framev1alpha1.FrameJobSpec{
				Pipeline:     "neura-training-dag",
				ServiceClass: "HIGH",
				Priority:     "high",
				Namespace:    "somewhere-else",
				GPUCount:     2,
			},
		}
		Expect(k8sClient.Create(ctx, alpha)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, alpha) })

		key := types.NamespacedName{Name: "conv-job", Namespace: "default"}

		By("reading it at the storage version")
		beta := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, beta)).To(Succeed())
		Expect(beta.Spec.Pipeline).To(Equal("neura-training-dag"))
		Expect(beta.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassHigh))
		Expect(beta.Spec.GPUCount).To(BeNumerically("==", 2))

		By("reading it back at v1alpha1 and seeing the normalised namespace")
		readBack := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, readBack)).To(Succeed())
		Expect(readBack.Spec.Namespace).To(Equal("default"),
			"a v1alpha1 client must see the namespace the operator acts in")
	})

	It("projects a phase out of conditions for a v1alpha1 reader", func() {
		beta := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-phase", Namespace: "default"},
			Spec:       framev1beta1.FrameJobSpec{Pipeline: "neura-inference-dag"},
		}
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })

		key := types.NamespacedName{Name: "conv-phase", Namespace: "default"}

		By("reading it at v1alpha1 before any controller has run")
		alpha := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, alpha)).To(Succeed())
		Expect(alpha.Status.Phase).To(Equal("Pending"),
			"no Ready condition means the controller has not seen it yet")

		By("setting a Ready condition at the storage version")
		Expect(k8sClient.Get(ctx, key, beta)).To(Succeed())
		beta.Status.Conditions = []metav1.Condition{{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             jobPhaseFailed,
			Message:            "workflow failed",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: beta.Generation,
		}}
		Expect(k8sClient.Status().Update(ctx, beta)).To(Succeed())

		Expect(k8sClient.Get(ctx, key, alpha)).To(Succeed())
		Expect(alpha.Status.Phase).To(Equal("Failed"))
	})

	It("moves a v1alpha1 FrameUser's password hash onto status", func() {
		// The one bijection in the whole freeze: v1alpha1.spec.passwordHash
		// <-> v1beta1.status.passwordHash (F11). It works on create even
		// though status is a subresource, because the apiserver prunes status
		// at the *request* version and only then converts to the storage
		// version — ConvertTo runs after the pruning, so what it puts in
		// status survives. If that ordering ever changed, a v1alpha1 client
		// setting a password would silently get an account it cannot log
		// into, which is why this is asserted through a real apiserver rather
		// than against the conversion function.
		hash := "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"
		alpha := &framev1alpha1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-user", Namespace: "default"},
			Spec: framev1alpha1.FrameUserSpec{
				Email:        "conv@example.test",
				Role:         "viewer",
				PasswordAuth: "enabled",
				PasswordHash: hash,
			},
		}
		Expect(k8sClient.Create(ctx, alpha)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, alpha) })

		beta := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "conv-user", Namespace: "default"}, beta)).
			To(Succeed())
		Expect(beta.Status.PasswordHash).To(Equal(hash),
			"the credential must land on the status subresource, not in spec")
	})

	It("accepts a v1beta1 FrameNode with no serviceClass at all", func() {
		beta := &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-node", Namespace: "default"},
			Spec: framev1beta1.FrameNodeSpec{
				IP:           "10.0.0.9",
				ServiceClass: framev1beta1.ServiceClass(""),
			},
		}
		// An empty string is the zero value and is omitted by omitempty, so
		// this actually asserts that *absence* is fine — the enum has no ""
		// member and absence is how "unclassified" is spelled now.
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })
	})
})
