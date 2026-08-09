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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
)

// The services-group half of what internal/controller/frame's
// conversion_roundtrip_test.go does, and for the same reason: only a real
// apiserver holding the rendered CRDs can show that the shape ConvertTo emits
// is one the shipping v1beta1 schema stores without pruning, and that the
// shape ConvertFrom emits is one v1alpha1 accepts.
//
// It matters more here than anywhere else, because FrameService is the kind
// whose v1beta1 spec.type gained a pattern the v1alpha1 one did not have
// (T8), and whose parameters gained a per-value length bound. A conversion
// that emitted a value outside either would be a write the apiserver refuses,
// and no Go-level round trip can see that.
var _ = Describe("FrameService v1alpha1 <-> v1beta1 conversion through the apiserver", func() {
	// live is the richest FrameService the API admits, since none exists on any
	// cluster to model: every optional field set, so nothing goes untested by
	// being absent.
	live := func(name string) *servicesv1alpha1.FrameService {
		return &servicesv1alpha1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: servicesv1alpha1.FrameServiceSpec{
				Type:           "inference",
				ServiceClass:   "HIGH",
				DeletionPolicy: "Retain",
				Parameters: map[string]string{
					"model":         "qwen3-coder-30b",
					"contextLength": "32768",
				},
				Binding: servicesv1alpha1.BindingSpec{
					SecretName: "inference-creds",
					ProjectTo:  []string{"neura", "default"},
				},
			},
			Status: servicesv1alpha1.FrameServiceStatus{
				ObservedGeneration: 4,
				Phase:              "Ready",
				Sizing: servicesv1alpha1.Sizing{
					GPU: "1", GPUMemory: "24Gi", CPU: "4", Memory: "16Gi",
				},
				Binding: servicesv1alpha1.BindingStatus{
					SecretRef: &corev1.LocalObjectReference{Name: "inference-creds"},
					Endpoint:  "http://inference.default.svc:8080",
					Projected: []servicesv1alpha1.ProjectedSecretRef{
						{Namespace: "neura", Name: "inference-creds"},
						{Namespace: "default", Name: "inference-creds"},
					},
				},
				Provisioned: []servicesv1alpha1.ProvisionedRef{
					{APIVersion: "apps/v1", Kind: "Deployment", Name: "inference", Namespace: "default"},
					{APIVersion: "v1", Kind: "Service", Name: "inference", Namespace: "default"},
				},
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "Provisioned",
					Message:            "inference instance serving",
					ObservedGeneration: 4,
					LastTransitionTime: metav1.Now().Rfc3339Copy(),
				}},
			},
		}
	}

	// writeThrough creates the object, writes the status it arrived carrying
	// through the subresource, and reads back what storage holds.
	writeThrough := func(obj client.Object, status func()) {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		status()
		Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
	}

	It("survives a trip through v1beta1 storage with only status.phase recomputed", func() {
		alpha := live("conv-svc")
		wantStatus := alpha.Status
		writeThrough(alpha, func() { alpha.Status = wantStatus })
		storedSpec, storedStatus := alpha.Spec, alpha.Status

		hub := &servicesv1beta1.FrameService{}
		Expect(alpha.ConvertTo(hub)).To(Succeed())
		hub.Name = "conv-svc-hub"
		hub.ResourceVersion = ""
		hub.UID = ""
		hub.Generation = 0
		hub.CreationTimestamp = metav1.Time{}
		hub.ManagedFields = nil
		hubStatus := hub.Status
		// If ConvertTo emitted a spec.type outside v1beta1's new pattern, or a
		// parameter value past 1024 characters, this write is where it fails —
		// loudly, rather than as a difference nobody reads.
		writeThrough(hub, func() { hub.Status = hubStatus })

		back := &servicesv1alpha1.FrameService{}
		Expect(back.ConvertFrom(hub)).To(Succeed())

		Expect(cmp.Diff(storedSpec, back.Spec)).To(BeEmpty(),
			"spec changed on a v1alpha1 -> v1beta1 -> v1alpha1 trip through the apiserver")
		// status.phase is the sole v1alpha1-only field, and on this object the
		// projection reconstructs the very value that was stored: Ready=True is
		// Ready. So the whole status compares equal, phase included — no
		// exclusion is needed and none is taken.
		Expect(cmp.Diff(storedStatus, back.Status)).To(BeEmpty(),
			"status changed on a v1alpha1 -> v1beta1 -> v1alpha1 trip through the apiserver")
		Expect(back.Status.Phase).To(Equal("Ready"))
		Expect(back.Annotations).To(BeEmpty(), "nothing is stashed in an annotation")
	})

	It("recomputes phase from Ready.Status, never from the diagnostic reason", func() {
		// The reason vocabulary is the trap: ModelCacheMissing is a real reason
		// the inference provider writes, and it is not a member of v1alpha1's
		// phase enum. A projection that read the reason would produce an object
		// the apiserver refuses to store — so this spec writes the projected
		// result back at v1alpha1 to prove it does not.
		alpha := live("conv-svc-degraded")
		alpha.Status.Phase = "Degraded"
		alpha.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ModelCacheMissing",
			Message:            "model qwen3-coder-30b is not in the cache",
			ObservedGeneration: 4,
			LastTransitionTime: metav1.Now().Rfc3339Copy(),
		}}
		wantStatus := alpha.Status
		writeThrough(alpha, func() { alpha.Status = wantStatus })

		hub := &servicesv1beta1.FrameService{}
		Expect(alpha.ConvertTo(hub)).To(Succeed())
		hub.Name = "conv-svc-degraded-hub"
		hub.ResourceVersion = ""
		hub.UID = ""
		hub.Generation = 0
		hub.CreationTimestamp = metav1.Time{}
		hub.ManagedFields = nil
		hubStatus := hub.Status
		writeThrough(hub, func() { hub.Status = hubStatus })

		back := &servicesv1alpha1.FrameService{}
		Expect(back.ConvertFrom(hub)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Degraded"),
			"Ready=False projects to Degraded; echoing the reason would emit ModelCacheMissing")

		// And the projected object is one v1alpha1 will actually store. This is
		// the assertion no Go test can make: the enum lives in the CRD.
		back.Name = "conv-svc-degraded-back"
		back.ResourceVersion = ""
		back.UID = ""
		back.Generation = 0
		back.CreationTimestamp = metav1.Time{}
		back.ManagedFields = nil
		backStatus := back.Status
		writeThrough(back, func() { back.Status = backStatus })
		Expect(back.Status.Phase).To(Equal("Degraded"),
			"the projected phase must survive a v1alpha1 write, so it must be inside the enum")
	})

	It("projects Deleting from the deletion timestamp, which no condition carries", func() {
		alpha := live("conv-svc-deleting")
		alpha.Finalizers = []string{"services.plume-labs.io/frameservice"}
		wantStatus := alpha.Status
		writeThrough(alpha, func() { alpha.Status = wantStatus })

		// A real deletion, so the timestamp is the apiserver's rather than one
		// the test made up: the finalizer is what keeps the object around to be
		// converted.
		Expect(k8sClient.Delete(ctx, alpha)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(alpha), alpha)).To(Succeed())
		Expect(alpha.DeletionTimestamp).NotTo(BeNil())
		DeferCleanup(func() {
			alpha.Finalizers = nil
			_ = k8sClient.Update(ctx, alpha)
		})

		hub := &servicesv1beta1.FrameService{}
		Expect(alpha.ConvertTo(hub)).To(Succeed())
		Expect(hub.DeletionTimestamp).NotTo(BeNil(),
			"ConvertTo copies ObjectMeta wholesale, so the timestamp must come with it")

		back := &servicesv1alpha1.FrameService{}
		Expect(back.ConvertFrom(hub)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Deleting"),
			"Ready is still True on this object; only the deletion timestamp says otherwise")
	})
})
