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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*, not about the TalosMachineConfig
// controller: the removal of talosSecretRef.namespace (F6), the required
// talosSecretRef.name (F7) and the inherited object-level configPatch rule are
// all enforced by the apiserver from the CRD, so nothing but a real apiserver
// can show them working. They run against the CRDs as kustomize renders them
// (renderedCRDPath), which is the artefact the cluster installs.
//
// Every rejection is paired with an acceptance. A pattern that rejects the
// legal value is a different bug from one that accepts everything, and a suite
// that only ever asserts failure cannot tell them apart.
//
// There is no live-object fixture here, unlike the FrameJob/FrameNode/quota
// suites: zero TalosMachineConfigs exist on any cluster, verified by
// `kubectl get talosmachineconfigs -A` before this was written. Nothing
// converts, so the shapes below are the sample and e2e shapes instead.
var _ = Describe("TalosMachineConfig v1beta1 schema", func() {
	// sampleShaped is modelled field-for-field on
	// config/samples/frame_v1alpha1_talosmachineconfig.yaml, which is the
	// closest thing to a real object this kind has.
	sampleShaped := func(name string) *framev1beta1.TalosMachineConfig {
		return &framev1beta1.TalosMachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.TalosMachineConfigSpec{
				NodeName:       "worker-01",
				TalosEndpoint:  "192.168.10.10:50000",
				TalosSecretRef: framev1beta1.TalosSecretReference{Name: "talos-client-certs"},
				ConfigPatch:    "machine:\n  sysctls:\n    vm.max_map_count: \"524288\"\n",
			},
		}
	}

	// rawSpec builds an unstructured TalosMachineConfig. Several of the markers
	// below cannot be reached through the typed client at all: talosSecretRef
	// and its name carry no omitempty, so a Go client always sends both keys,
	// and only an object that omits a key entirely reaches the required-fields
	// check.
	rawSpec := func(name string, spec map[string]any) *unstructured.Unstructured {
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("TalosMachineConfig"))
		raw.SetNamespace("default")
		raw.SetName(name)
		Expect(unstructured.SetNestedMap(raw.Object, spec, "spec")).To(Succeed())
		return raw
	}

	It("holds a sample-shaped object without loss, status included", func() {
		tmc := sampleShaped("sample-shaped-tmc")
		Expect(k8sClient.Create(ctx, tmc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, tmc) })

		key := types.NamespacedName{Name: "sample-shaped-tmc", Namespace: "default"}

		// A lagging observedGeneration is the state a conversion has to carry,
		// so it is the state written here rather than a tidy matching pair.
		// Part 0's Task 2 made the controller's success path depend on this
		// field: without the guard the first successful reconcile re-ran
		// ApplyConfiguration against a live machine.
		tmc.Status.ObservedGeneration = 1
		tmc.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "ConfigApplied",
			Message:            "Applied to worker-01",
			ObservedGeneration: 1,
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, tmc)).To(Succeed())

		back := &framev1beta1.TalosMachineConfig{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.NodeName).To(Equal("worker-01"))
		Expect(back.Spec.TalosEndpoint).To(Equal("192.168.10.10:50000"))
		Expect(back.Spec.TalosSecretRef.Name).To(Equal("talos-client-certs"))
		Expect(back.Spec.ConfigPatch).To(ContainSubstring("vm.max_map_count"))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 1))
		Expect(back.Status.Conditions).To(HaveLen(1))
		Expect(back.Status.Conditions[0].ObservedGeneration).To(BeNumerically("==", 1))
	})

	It("accepts a configPatchRef instead of an inline patch", func() {
		tmc := sampleShaped("patchref-tmc")
		tmc.Spec.ConfigPatch = ""
		tmc.Spec.ConfigPatchRef = &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "patch-cm"},
			Key:                  "patch.yaml",
		}
		Expect(k8sClient.Create(ctx, tmc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, tmc) })

		back := &framev1beta1.TalosMachineConfig{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "patchref-tmc", Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.ConfigPatchRef).NotTo(BeNil())
		Expect(back.Spec.ConfigPatchRef.Name).To(Equal("patch-cm"))
	})

	// ---- F6: talosSecretRef.namespace is gone ----

	It("drops a talosSecretRef.namespace written on the wire (F6)", func() {
		// The field is removed from the schema rather than rejected, which is
		// what removing a property from a structural schema does: the apiserver
		// prunes unknown keys. The observable contract is that a client cannot
		// make v1beta1 name another namespace — it comes back absent — and that
		// is what this pins. Only an unstructured object can put the key on the
		// wire at all; the typed struct has no field for it.
		raw := rawSpec("f6-namespace-pruned", map[string]any{
			"nodeName":      "worker-01",
			"talosEndpoint": "192.168.10.10:50000",
			"talosSecretRef": map[string]any{
				"name":      "talos-client-certs",
				"namespace": "kube-system",
			},
			"configPatch": "machine: {}\n",
		})
		Expect(k8sClient.Create(ctx, raw)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })

		back := &unstructured.Unstructured{}
		back.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("TalosMachineConfig"))
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "f6-namespace-pruned", Namespace: "default"}, back)).To(Succeed())

		_, found, err := unstructured.NestedString(back.Object, "spec", "talosSecretRef", "namespace")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "v1beta1 must not carry talosSecretRef.namespace (F6)")

		name, found, err := unstructured.NestedString(back.Object, "spec", "talosSecretRef", "name")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(name).To(Equal("talos-client-certs"))
	})

	It("still accepts talosSecretRef.namespace on v1alpha1, but storage no longer keeps it (F6)", func() {
		// The counterpart to the spec above, and the pair is the whole of F6's
		// compatibility story. v1alpha1 keeps the *field*: a client that names
		// a namespace is still admitted, so no existing manifest or CI
		// pipeline starts erroring. What it does not keep is the *value* —
		// v1beta1 is the storage version now, ConvertTo has nowhere to put a
		// namespace, and the read back comes through ConvertFrom with the
		// field empty. Silent data loss on one field of one spoke version is
		// the price of the fix; refusing the write instead would have broken
		// every caller on the day the operator upgraded.
		old := &framev1alpha1.TalosMachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "f6-alpha-keeps-ns", Namespace: "default"},
			Spec: framev1alpha1.TalosMachineConfigSpec{
				NodeName:      "worker-01",
				TalosEndpoint: "192.168.10.10:50000",
				TalosSecretRef: framev1alpha1.TalosSecretReference{
					Name:      "talos-client-certs",
					Namespace: "kube-system",
				},
				ConfigPatch: "machine: {}\n",
			},
		}
		Expect(k8sClient.Create(ctx, old)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, old) })

		back := &framev1alpha1.TalosMachineConfig{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "f6-alpha-keeps-ns", Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.TalosSecretRef.Name).To(Equal("talos-client-certs"),
			"everything v1beta1 still models must survive the round trip")
		Expect(back.Spec.TalosSecretRef.Namespace).To(BeEmpty(),
			"the namespace has nowhere to live at the storage version, so it does not come back")
	})

	// ---- F7: talosSecretRef.name is Required ----

	It("rejects a talosSecretRef with no name key at all, which is what Required buys (F7)", func() {
		// Nothing typed can test this. `name` is a non-pointer string, so a Go
		// client setting "" still sends the key and the pattern rejects it
		// first — every typed entry below passes whether the field is required
		// or not. Only an object that omits the key reaches the
		// required-fields check.
		//
		// Note what "deleting the marker" means here, because it is not what
		// it looks like: in this controller-gen a field is required unless it
		// carries +optional, and the json tag's omitempty is irrelevant, so
		// +kubebuilder:validation:Required restates the default and stripping
		// it changes no byte of the generated schema. The mutation that makes
		// this spec fail is Required -> +optional, and it does.
		raw := rawSpec("f7-absent-name", map[string]any{
			"nodeName":       "worker-01",
			"talosEndpoint":  "192.168.10.10:50000",
			"talosSecretRef": map[string]any{},
			"configPatch":    "machine: {}\n",
		})
		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted a talosSecretRef with no name")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring("name"),
			"the rejection must name talosSecretRef.name, not some other rule")

		// The counterpart: the very same spec with the key supplied is
		// accepted, so the rejection above is about the missing key and not
		// about the endpoint, the node name or the object-level rule.
		ok := rawSpec("f7-present-name", map[string]any{
			"nodeName":       "worker-01",
			"talosEndpoint":  "192.168.10.10:50000",
			"talosSecretRef": map[string]any{"name": "talos-client-certs"},
			"configPatch":    "machine: {}\n",
		})
		Expect(k8sClient.Create(ctx, ok)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ok) })
	})

	It("accepts a v1alpha1 object with no talosSecretRef.name, so Required is v1beta1's alone (F7)", func() {
		// F7's whole point: v1alpha1 admits this and fails later at reconcile
		// with a ClientBuildFailed condition. This spec is what makes the
		// v1beta1 rejection above a *change* rather than a restatement.
		old := &framev1alpha1.TalosMachineConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "f7-alpha-unnamed", Namespace: "default"},
			Spec: framev1alpha1.TalosMachineConfigSpec{
				NodeName:       "worker-01",
				TalosEndpoint:  "192.168.10.10:50000",
				TalosSecretRef: framev1alpha1.TalosSecretReference{},
				ConfigPatch:    "machine: {}\n",
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
	// pattern or the inner required check rejects it first. Deleting the
	// requiredness would leave a typed spec passing.
	completeSpec := func() map[string]any {
		return map[string]any{
			"nodeName":       "worker-01",
			"talosEndpoint":  "192.168.10.10:50000",
			"talosSecretRef": map[string]any{"name": "talos-client-certs"},
			"configPatch":    "machine: {}\n",
		}
	}

	It("accepts completeSpec, so every omission below is about the missing key alone", func() {
		Expect(k8sClient.Create(ctx, rawSpec("complete-spec-tmc", completeSpec()))).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, rawSpec("complete-spec-tmc", completeSpec()))
		})
	})

	DescribeTable("rejects a spec with a required key omitted entirely",
		func(key string) {
			spec := completeSpec()
			delete(spec, key)
			raw := rawSpec("omit-"+strings.ToLower(key), spec)
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
	)

	DescribeTable("rejects a spec the freeze forbids",
		func(name string, mutate func(*framev1beta1.TalosMachineConfigSpec)) {
			tmc := sampleShaped(name)
			mutate(&tmc.Spec)
			err := k8sClient.Create(ctx, tmc)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, tmc) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec the schema must reject")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		},
		// F7's field-level half: an empty name is not a name.
		Entry("an empty talosSecretRef.name (F7)", "reject-empty-secret-name",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosSecretRef.Name = "" }),
		Entry("a talosSecretRef.name that is not a Kubernetes object name (F7)", "reject-malformed-secret-name",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosSecretRef.Name = "Talos_Certs" }),
		Entry("a talosSecretRef.name longer than 253 characters (F7)", "reject-long-secret-name",
			func(s *framev1beta1.TalosMachineConfigSpec) {
				s.TalosSecretRef.Name = strings.Repeat("a", 254)
			}),
		// Carried over from v1alpha1 unchanged, pinned so a later task cannot
		// drop them while rewriting the type.
		Entry("an empty nodeName", "reject-empty-node",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.NodeName = "" }),
		Entry("a nodeName that is not a DNS name", "reject-malformed-node",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.NodeName = "Worker_01" }),
		Entry("a nodeName longer than 253 characters", "reject-long-node",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.NodeName = strings.Repeat("a", 254) }),
		Entry("a talosEndpoint with no port", "reject-portless-endpoint",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosEndpoint = "192.168.10.10" }),
		Entry("a talosEndpoint with a bare IPv6 address", "reject-unbracketed-v6",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosEndpoint = "fd00::1:50000" }),
		Entry("an empty talosEndpoint", "reject-empty-endpoint",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosEndpoint = "" }),
		// The inherited object-level rule, which the freeze makes permanent.
		Entry("neither configPatch nor configPatchRef", "reject-no-patch",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.ConfigPatch = "" }),
		Entry("both configPatch and configPatchRef", "reject-both-patches",
			func(s *framev1beta1.TalosMachineConfigSpec) {
				s.ConfigPatchRef = &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "patch-cm"},
					Key:                  "patch.yaml",
				}
			}),
	)

	It("rejects an explicitly empty configPatch alongside no ref, so the rule is not a presence check", func() {
		// The rule reads `has(self.configPatch) && size(self.configPatch) > 0`,
		// not `has(self.configPatch)`. Through the typed client that
		// distinction is invisible — omitempty means "" never reaches the wire
		// — so it takes an unstructured object to show that a present-but-empty
		// patch is rejected rather than accepted as "the key is there".
		raw := rawSpec("reject-explicit-empty-patch", map[string]any{
			"nodeName":       "worker-01",
			"talosEndpoint":  "192.168.10.10:50000",
			"talosSecretRef": map[string]any{"name": "talos-client-certs"},
			"configPatch":    "",
		})
		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), `the apiserver accepted configPatch: ""`)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
	})

	DescribeTable("accepts a spec sitting exactly on each bound",
		func(name string, mutate func(*framev1beta1.TalosMachineConfigSpec)) {
			tmc := sampleShaped(name)
			mutate(&tmc.Spec)
			Expect(k8sClient.Create(ctx, tmc)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tmc) })
		},
		Entry("a one-character talosSecretRef.name", "accept-short-secret-name",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosSecretRef.Name = "a" }),
		Entry("a talosSecretRef.name of exactly 253 characters", "accept-max-secret-name",
			func(s *framev1beta1.TalosMachineConfigSpec) {
				s.TalosSecretRef.Name = strings.Repeat("a", 253)
			}),
		Entry("a dotted talosSecretRef.name", "accept-dotted-secret-name",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosSecretRef.Name = "talos.client.certs" }),
		Entry("a nodeName of exactly 253 characters", "accept-max-node",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.NodeName = strings.Repeat("a", 253) }),
		Entry("a bracketed IPv6 talosEndpoint", "accept-bracketed-v6",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosEndpoint = "[fd00::1]:50000" }),
		Entry("a hostname talosEndpoint", "accept-hostname-endpoint",
			func(s *framev1beta1.TalosMachineConfigSpec) { s.TalosEndpoint = "worker-01.talos.local:50000" }),
	)
})
