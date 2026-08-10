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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*, not about the TalosUpgrade
// controller. TalosUpgrade shares TalosSecretReference with TalosMachineConfig,
// so F6 and F7 are asserted here too: a shared type broken on one kind and
// intact on the other is a state the suites must be able to distinguish.
//
// Unlike TalosMachineConfig this kind has no object-level CEL rule on either
// version — its one rule bounds spec.image and lives on that field, where
// ratcheting can reach it. That asymmetry is pinned in the generated CRD, not
// here, but it is why nothing below asserts a spec-level rejection.
//
// Zero TalosUpgrades exist on any cluster, verified by
// `kubectl get talosupgrades -A` before this was written, so the shapes below
// come from the sample and the e2e fixture rather than from a stored object.
var _ = Describe("TalosUpgrade v1beta1 schema", func() {
	sampleShaped := func(name string) *framev1beta1.TalosUpgrade {
		return &framev1beta1.TalosUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.TalosUpgradeSpec{
				NodeName:       "worker-01",
				TalosEndpoint:  "192.168.10.10:50000",
				TalosSecretRef: framev1beta1.TalosSecretReference{Name: "talos-client-certs"},
				Image:          "ghcr.io/siderolabs/talos:v1.9.0",
			},
		}
	}

	rawSpec := func(name string, spec map[string]any) *unstructured.Unstructured {
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("TalosUpgrade"))
		raw.SetNamespace("default")
		raw.SetName(name)
		Expect(unstructured.SetNestedMap(raw.Object, spec, "spec")).To(Succeed())
		return raw
	}

	It("holds a sample-shaped object without loss, status included", func() {
		tu := sampleShaped("sample-shaped-tu")
		Expect(k8sClient.Create(ctx, tu)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, tu) })

		key := types.NamespacedName{Name: "sample-shaped-tu", Namespace: "default"}

		tu.Status.ObservedGeneration = 1
		tu.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "UpgradeRequested",
			Message:            "Upgrade requested on worker-01",
			ObservedGeneration: 1,
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, tu)).To(Succeed())

		back := &framev1beta1.TalosUpgrade{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.NodeName).To(Equal("worker-01"))
		Expect(back.Spec.TalosEndpoint).To(Equal("192.168.10.10:50000"))
		Expect(back.Spec.TalosSecretRef.Name).To(Equal("talos-client-certs"))
		Expect(back.Spec.Image).To(Equal("ghcr.io/siderolabs/talos:v1.9.0"))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 1))
		Expect(back.Status.Conditions).To(HaveLen(1))
		Expect(back.Status.Conditions[0].ObservedGeneration).To(BeNumerically("==", 1))
	})

	// ---- F6 / F7 on the shared type, asserted for this kind too ----

	It("drops a talosSecretRef.namespace written on the wire (F6)", func() {
		raw := rawSpec("tu-f6-namespace-pruned", map[string]any{
			"nodeName":      "worker-01",
			"talosEndpoint": "192.168.10.10:50000",
			"talosSecretRef": map[string]any{
				"name":      "talos-client-certs",
				"namespace": "kube-system",
			},
			"image": "ghcr.io/siderolabs/talos:v1.9.0",
		})
		Expect(k8sClient.Create(ctx, raw)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })

		back := &unstructured.Unstructured{}
		back.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("TalosUpgrade"))
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "tu-f6-namespace-pruned", Namespace: "default"}, back)).To(Succeed())

		_, found, err := unstructured.NestedString(back.Object, "spec", "talosSecretRef", "namespace")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "v1beta1 must not carry talosSecretRef.namespace (F6)")
	})

	It("still accepts talosSecretRef.namespace on v1alpha1, but storage no longer keeps it (F6)", func() {
		// As on TalosMachineConfig: the field stays admissible on v1alpha1 so
		// no existing manifest starts erroring, but v1beta1 is the storage
		// version and has nowhere to put the value, so it does not survive
		// the round trip.
		old := &framev1alpha1.TalosUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: "tu-f6-alpha-keeps-ns", Namespace: "default"},
			Spec: framev1alpha1.TalosUpgradeSpec{
				NodeName:      "worker-01",
				TalosEndpoint: "192.168.10.10:50000",
				TalosSecretRef: framev1alpha1.TalosSecretReference{
					Name:      "talos-client-certs",
					Namespace: "kube-system",
				},
				Image: "ghcr.io/siderolabs/talos:v1.9.0",
			},
		}
		Expect(k8sClient.Create(ctx, old)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, old) })

		back := &framev1alpha1.TalosUpgrade{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "tu-f6-alpha-keeps-ns", Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.TalosSecretRef.Name).To(Equal("talos-client-certs"),
			"everything v1beta1 still models must survive the round trip")
		Expect(back.Spec.TalosSecretRef.Namespace).To(BeEmpty(),
			"the namespace has nowhere to live at the storage version, so it does not come back")
	})

	It("rejects a talosSecretRef with no name key at all, which is what Required buys (F7)", func() {
		// As on TalosMachineConfig: `name` is a non-pointer string, so a typed
		// client setting "" still sends the key and the pattern rejects it
		// first. Only an object that omits the key reaches the required-fields
		// check — and the mutation that makes this spec fail is
		// Required -> +optional, not stripping Required, which in this
		// controller-gen only restates the default.
		raw := rawSpec("tu-f7-absent-name", map[string]any{
			"nodeName":       "worker-01",
			"talosEndpoint":  "192.168.10.10:50000",
			"talosSecretRef": map[string]any{},
			"image":          "ghcr.io/siderolabs/talos:v1.9.0",
		})
		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted a talosSecretRef with no name")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring("name"),
			"the rejection must name talosSecretRef.name, not some other rule")

		ok := rawSpec("tu-f7-present-name", map[string]any{
			"nodeName":       "worker-01",
			"talosEndpoint":  "192.168.10.10:50000",
			"talosSecretRef": map[string]any{"name": "talos-client-certs"},
			"image":          "ghcr.io/siderolabs/talos:v1.9.0",
		})
		Expect(k8sClient.Create(ctx, ok)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ok) })
	})

	It("accepts a v1alpha1 object with no talosSecretRef.name, so Required is v1beta1's alone (F7)", func() {
		old := &framev1alpha1.TalosUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: "tu-f7-alpha-unnamed", Namespace: "default"},
			Spec: framev1alpha1.TalosUpgradeSpec{
				NodeName:       "worker-01",
				TalosEndpoint:  "192.168.10.10:50000",
				TalosSecretRef: framev1alpha1.TalosSecretReference{},
				Image:          "ghcr.io/siderolabs/talos:v1.9.0",
			},
		}
		Expect(k8sClient.Create(ctx, old)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, old) })
	})

	// completeSpec is the whole valid spec as a map. The table below deletes
	// one key from a copy of it and asserts the rejection names that key. None
	// of these are reachable through the typed client: every one of these
	// fields is a non-pointer with no omitempty, so a Go client always sends
	// the key — with `""` for the strings and `{}` for the struct — and the
	// pattern, the image tag rule or the inner required check rejects it
	// first. Deleting the requiredness would leave a typed spec passing.
	completeSpec := func() map[string]any {
		return map[string]any{
			"nodeName":       "worker-01",
			"talosEndpoint":  "192.168.10.10:50000",
			"talosSecretRef": map[string]any{"name": "talos-client-certs"},
			"image":          "ghcr.io/siderolabs/talos:v1.9.0",
		}
	}

	It("accepts completeSpec, so every omission below is about the missing key alone", func() {
		Expect(k8sClient.Create(ctx, rawSpec("complete-spec-tu", completeSpec()))).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, rawSpec("complete-spec-tu", completeSpec()))
		})
	})

	DescribeTable("rejects a spec with a required key omitted entirely",
		func(key string) {
			spec := completeSpec()
			delete(spec, key)
			raw := rawSpec("tu-omit-"+strings.ToLower(key), spec)
			err := k8sClient.Create(ctx, raw)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec with no %s", key)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
			Expect(err.Error()).To(ContainSubstring(key),
				"the rejection must name %s, not some other rule", key)
		},
		Entry("nodeName", "nodeName"),
		Entry("talosEndpoint", "talosEndpoint"),
		Entry("talosSecretRef", "talosSecretRef"),
		Entry("image", "image"),
	)

	DescribeTable("rejects a spec the freeze forbids",
		func(name string, mutate func(*framev1beta1.TalosUpgradeSpec)) {
			tu := sampleShaped(name)
			mutate(&tu.Spec)
			err := k8sClient.Create(ctx, tu)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, tu) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec the schema must reject")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		},
		Entry("an empty talosSecretRef.name (F7)", "tu-reject-empty-secret-name",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosSecretRef.Name = "" }),
		Entry("a talosSecretRef.name that is not a Kubernetes object name (F7)", "tu-reject-malformed-secret-name",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosSecretRef.Name = "Talos_Certs" }),
		Entry("a talosSecretRef.name longer than 253 characters (F7)", "tu-reject-long-secret-name",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosSecretRef.Name = strings.Repeat("a", 254) }),
		Entry("an empty nodeName", "tu-reject-empty-node",
			func(s *framev1beta1.TalosUpgradeSpec) { s.NodeName = "" }),
		Entry("a nodeName that is not a DNS name", "tu-reject-malformed-node",
			func(s *framev1beta1.TalosUpgradeSpec) { s.NodeName = "Worker_01" }),
		Entry("a nodeName longer than 253 characters", "tu-reject-long-node",
			func(s *framev1beta1.TalosUpgradeSpec) { s.NodeName = strings.Repeat("a", 254) }),
		Entry("a talosEndpoint with no port", "tu-reject-portless-endpoint",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosEndpoint = "192.168.10.10" }),
		Entry("a talosEndpoint with a bare IPv6 address", "tu-reject-unbracketed-v6",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosEndpoint = "fd00::1:50000" }),
		// The field-level image rule, carried over from v1alpha1 unchanged.
		Entry("an untagged image", "tu-reject-untagged-image",
			func(s *framev1beta1.TalosUpgradeSpec) { s.Image = "ghcr.io/siderolabs/talos" }),
		Entry("an image whose only colon is in the registry port", "tu-reject-registry-port-only",
			func(s *framev1beta1.TalosUpgradeSpec) { s.Image = "registry:5000/siderolabs/talos" }),
		Entry("an empty image", "tu-reject-empty-image",
			func(s *framev1beta1.TalosUpgradeSpec) { s.Image = "" }),
		Entry("an image longer than 255 characters", "tu-reject-long-image",
			func(s *framev1beta1.TalosUpgradeSpec) {
				s.Image = "ghcr.io/siderolabs/" + strings.Repeat("a", 240) + ":v1.9.0"
			}),
	)

	DescribeTable("accepts a spec sitting exactly on each bound",
		func(name string, mutate func(*framev1beta1.TalosUpgradeSpec)) {
			tu := sampleShaped(name)
			mutate(&tu.Spec)
			Expect(k8sClient.Create(ctx, tu)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tu) })
		},
		Entry("a one-character talosSecretRef.name", "tu-accept-short-secret-name",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosSecretRef.Name = "a" }),
		Entry("a talosSecretRef.name of exactly 253 characters", "tu-accept-max-secret-name",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosSecretRef.Name = strings.Repeat("a", 253) }),
		Entry("a nodeName of exactly 253 characters", "tu-accept-max-node",
			func(s *framev1beta1.TalosUpgradeSpec) { s.NodeName = strings.Repeat("a", 253) }),
		Entry("a bracketed IPv6 talosEndpoint", "tu-accept-bracketed-v6",
			func(s *framev1beta1.TalosUpgradeSpec) { s.TalosEndpoint = "[fd00::1]:50000" }),
		Entry("a tagged image on a registry with a port", "tu-accept-registry-port",
			func(s *framev1beta1.TalosUpgradeSpec) { s.Image = "registry:5000/siderolabs/talos:v1.9.0" }),
		Entry("an image of exactly 255 characters", "tu-accept-max-image",
			func(s *framev1beta1.TalosUpgradeSpec) {
				s.Image = "ghcr.io/siderolabs/" + strings.Repeat("a", 229) + ":v1.9.0"
			}),
	)
})
