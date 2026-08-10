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

package services

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
)

// The FrameService half of the conversion specs in
// internal/controller/frame/conversion_envtest_test.go; the rationale is
// there and is not repeated. What is specific to this kind: its phase
// projection reads Ready.Status and the deletion timestamp, never
// Ready.Reason, because FrameService's reasons are diagnostic strings that
// are not members of v1alpha1's phase enum (see api/services/v1alpha1/phase.go).
var _ = Describe("Conversion", func() {
	ctx := context.Background()

	It("reports Pending to a v1alpha1 reader before the controller has run", func() {
		beta := &servicesv1beta1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-conv-pending", Namespace: "default"},
			Spec:       servicesv1beta1.FrameServiceSpec{Type: "inference"},
		}
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })

		alpha := &servicesv1alpha1.FrameService{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fs-conv-pending", Namespace: "default"}, alpha)).To(Succeed())
		Expect(alpha.Status.Phase).To(Equal("Pending"),
			"no conditions at all is what an object looks like before its first reconcile")
	})

	It("reports Ready to a v1alpha1 reader once Ready is True at the storage version", func() {
		beta := &servicesv1beta1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-conv-ready", Namespace: "default"},
			Spec:       servicesv1beta1.FrameServiceSpec{Type: "inference"},
		}
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })

		key := types.NamespacedName{Name: "fs-conv-ready", Namespace: "default"}
		Expect(k8sClient.Get(ctx, key, beta)).To(Succeed())
		beta.Status.Conditions = []metav1.Condition{{
			Type:   "Ready",
			Status: metav1.ConditionTrue,
			// A diagnostic reason, deliberately not a phase name: it must not
			// leak into status.phase.
			Reason:             "Provisioned",
			Message:            "serving",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: beta.Generation,
		}}
		Expect(k8sClient.Status().Update(ctx, beta)).To(Succeed())

		alpha := &servicesv1alpha1.FrameService{}
		Expect(k8sClient.Get(ctx, key, alpha)).To(Succeed())
		Expect(alpha.Status.Phase).To(Equal("Ready"))
		Expect(alpha.Status.Conditions).To(HaveLen(1))
		Expect(alpha.Status.Conditions[0].Reason).To(Equal("Provisioned"),
			"the reason stays a reason; it is not what the phase is computed from")
	})
})
