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
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
	"github.com/rmocq/frame/internal/scheduling"
)

// Retain and Delete are spelled often enough below that goconst asks for a
// name; they are the two members of the deletionPolicy enum.
const (
	policyRetain = "Retain"
	policyDelete = "Delete"
)

// These specs are about the v1beta1 *schema* of FrameService, the last of the
// eight kinds to gain a hub type. They install no webhook — the provider
// registry check and the parameter JSON Schema live in
// internal/webhook/services/v1alpha1 and have their own suite — so everything
// asserted here is what the apiserver enforces from the CRD alone.
//
// Four decisions are pinned.
//
//   - F2: status.phase is gone from v1beta1. Ready carries it, and the reason
//     names the real failure rather than being flattened into a five-valued
//     enum.
//   - F4/F10: spec.serviceClass becomes the shared framev1beta1.ServiceClass,
//     keeping its MEDIUM default (FrameJob's is LOW, deliberately). It is
//     also the instance's scheduling priority, so the enum and
//     internal/scheduling's mapping must not drift apart.
//   - T4: spec.parameters is bounded as an envelope — 64 entries, 1024
//     characters per value — with key *form* deliberately unconstrained.
//   - T8: spec.type gains MaxLength=63 and a lowercase-alphanumeric pattern.
//
// Every bound is asserted twice: a value one past it the apiserver must
// reject, and a value sitting exactly on it that it must accept. Every
// v1beta1-only rule is paired with the same write succeeding on v1alpha1, so
// the rule is demonstrably a change rather than a restatement.
//
// Zero FrameServices exist on any cluster — `kubectl get frameservices -A`
// returned nothing before this was written, and the CRD's storedVersions is
// ["v1alpha1"] — so every shape below comes from the sample, not from a
// stored object.
var _ = Describe("FrameService v1beta1 schema", func() {
	const schemaNS = "default"
	const crdName = "frameservices.services.plume-labs.io"

	sampleShaped := func(name string) *servicesv1beta1.FrameService {
		return &servicesv1beta1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: schemaNS},
			Spec: servicesv1beta1.FrameServiceSpec{
				Type: "inference",
			},
		}
	}

	rawSpec := func(name string, spec map[string]any) *unstructured.Unstructured {
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(servicesv1beta1.GroupVersion.WithKind("FrameService"))
		raw.SetNamespace(schemaNS)
		raw.SetName(name)
		Expect(unstructured.SetNestedMap(raw.Object, spec, "spec")).To(Succeed())
		return raw
	}

	// rejects is the shared shape of every negative spec: the object must be
	// refused by validation, and the message must name the field under test
	// rather than some unrelated rule firing first.
	rejects := func(obj client.Object, mustMention string) {
		err := k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted an object the schema must reject")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring(mustMention),
			"the rejection must name %s, not some other rule", mustMention)
	}

	accepts := func(obj client.Object) {
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
	}

	// servedSchemas reads the CRD off the running apiserver, so the specs
	// below follow Task 19's storage-version promotion and F13's conversion
	// wiring rather than restating today's answer.
	servedSchemas := func() map[string]map[string]any {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "apiextensions.k8s.io",
			Version: "v1",
			Kind:    "CustomResourceDefinition",
		})
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, crd)).To(Succeed())

		versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		out := map[string]map[string]any{}
		for _, v := range versions {
			m, ok := v.(map[string]any)
			Expect(ok).To(BeTrue())
			name, _ := m["name"].(string)
			s, _, err := unstructured.NestedMap(m, "schema", "openAPIV3Schema")
			Expect(err).NotTo(HaveOccurred())
			out[name] = s
		}
		Expect(out).To(HaveKey("v1alpha1"))
		Expect(out).To(HaveKey("v1beta1"))
		return out
	}

	It("holds a sample-shaped object without loss, status included", func() {
		svc := sampleShaped("fs-sample-shaped")
		svc.Spec.Parameters = map[string]framev1beta1.ParameterValue{"model": "llama-3-8b"}
		svc.Spec.ServiceClass = framev1beta1.ServiceClassHigh
		svc.Spec.DeletionPolicy = policyDelete
		svc.Spec.Binding = servicesv1beta1.BindingSpec{
			SecretName: "fs-sample-shaped-creds",
			ProjectTo:  []string{"team-a", "team-b"},
		}
		accepts(svc)

		key := types.NamespacedName{Name: "fs-sample-shaped", Namespace: schemaNS}

		svc.Status.ObservedGeneration = 3
		svc.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ModelCacheMissing",
			Message:            "the model has not finished downloading",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: 3,
		}}
		svc.Status.Binding = servicesv1beta1.BindingStatus{
			SecretRef: &corev1.LocalObjectReference{Name: "fs-sample-shaped-creds"},
			Endpoint:  "http://fs-sample-shaped.default.svc:8080",
			Projected: []servicesv1beta1.ProjectedSecretRef{
				{Namespace: "team-a", Name: "fs-sample-shaped-creds"},
			},
		}
		svc.Status.Sizing = servicesv1beta1.Sizing{GPU: "1", GPUMemory: "16Gi", CPU: "4", Memory: "32Gi"}
		svc.Status.Provisioned = []servicesv1beta1.ProvisionedRef{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "fs-sample-shaped", Namespace: schemaNS},
		}
		Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())

		back := &servicesv1beta1.FrameService{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.Type).To(Equal("inference"))
		Expect(back.Spec.Parameters).To(HaveKeyWithValue("model", framev1beta1.ParameterValue("llama-3-8b")))
		Expect(back.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassHigh))
		Expect(back.Spec.DeletionPolicy).To(Equal(policyDelete))
		Expect(back.Spec.Binding.SecretName).To(Equal("fs-sample-shaped-creds"))
		Expect(back.Spec.Binding.ProjectTo).To(ConsistOf("team-a", "team-b"))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 3))
		Expect(back.Status.Conditions).To(HaveLen(1))
		Expect(back.Status.Conditions[0].Reason).To(Equal("ModelCacheMissing"))
		Expect(back.Status.Binding.SecretRef).NotTo(BeNil())
		Expect(back.Status.Binding.SecretRef.Name).To(Equal("fs-sample-shaped-creds"))
		Expect(back.Status.Binding.Endpoint).To(Equal("http://fs-sample-shaped.default.svc:8080"))
		Expect(back.Status.Binding.Projected).To(HaveLen(1))
		Expect(back.Status.Sizing.GPUMemory).To(Equal("16Gi"))
		Expect(back.Status.Provisioned).To(HaveLen(1))
		Expect(back.Status.Provisioned[0].Kind).To(Equal("Deployment"))
	})

	// ---- F2: status.phase is gone ----

	It("prunes a status.phase written on the wire at v1beta1 (F2)", func() {
		svc := sampleShaped("fs-f2-phase-pruned")
		accepts(svc)

		key := types.NamespacedName{Name: "fs-f2-phase-pruned", Namespace: schemaNS}
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(servicesv1beta1.GroupVersion.WithKind("FrameService"))
		Expect(k8sClient.Get(ctx, key, raw)).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, "Ready", "status", "phase")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, raw)).To(Succeed())

		back := &unstructured.Unstructured{}
		back.SetGroupVersionKind(servicesv1beta1.GroupVersion.WithKind("FrameService"))
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		_, found, err := unstructured.NestedString(back.Object, "status", "phase")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "v1beta1 must not carry status.phase (F2)")

		// The replacement is asserted in the same breath, so the spec pins
		// what took phase's place and not merely that something is missing.
		observed, found, err := unstructured.NestedInt64(back.Object, "status", "observedGeneration")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "nothing wrote observedGeneration yet")
		Expect(observed).To(BeZero())

		typed := &servicesv1beta1.FrameService{}
		Expect(k8sClient.Get(ctx, key, typed)).To(Succeed())
		typed.Status.ObservedGeneration = 1
		typed.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "UnknownType",
			Message:            "no provider is registered for this type",
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, typed)).To(Succeed())

		Expect(k8sClient.Get(ctx, key, typed)).To(Succeed())
		Expect(typed.Status.ObservedGeneration).To(BeNumerically("==", 1))
		Expect(typed.Status.Conditions).To(HaveLen(1))
		Expect(typed.Status.Conditions[0].Reason).To(Equal("UnknownType"))
	})

	It("still stores status.phase on v1alpha1, so the removal is v1beta1's alone (F2)", func() {
		old := &servicesv1alpha1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-f2-alpha-keeps-phase", Namespace: schemaNS},
			Spec:       servicesv1alpha1.FrameServiceSpec{Type: "inference"},
		}
		accepts(old)

		key := types.NamespacedName{Name: "fs-f2-alpha-keeps-phase", Namespace: schemaNS}
		old.Status.Phase = "Ready"
		Expect(k8sClient.Status().Update(ctx, old)).To(Succeed())

		back := &servicesv1alpha1.FrameService{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Ready"),
			"v1alpha1 keeps phase — it is the storage version and nothing may drop it under it")
	})

	// The other seven kinds make v1beta1 a strict subset of v1alpha1, and so
	// does this one. That is the whole reason Task 18 can convert without an
	// annotation escape hatch: there is no v1beta1 field for a round trip
	// through v1alpha1 to lose. Measured against the served CRD so it keeps
	// answering after either version changes, rather than restating a diff
	// taken once by hand.
	It("declares no property v1alpha1 lacks, and lacks exactly status.phase (F2)", func() {
		schemas := servedSchemas()

		var paths func(node map[string]any, prefix string) map[string]bool
		paths = func(node map[string]any, prefix string) map[string]bool {
			out := map[string]bool{}
			props, _ := node["properties"].(map[string]any)
			for k, v := range props {
				child, ok := v.(map[string]any)
				if !ok {
					continue
				}
				p := prefix + k
				if !strings.HasPrefix(p, "metadata") && p != "apiVersion" && p != "kind" {
					out[p] = true
				}
				for sub := range paths(child, p+".") {
					out[sub] = true
				}
			}
			if items, ok := node["items"].(map[string]any); ok {
				for sub := range paths(items, prefix) {
					out[sub] = true
				}
			}
			if add, ok := node["additionalProperties"].(map[string]any); ok {
				for sub := range paths(add, prefix) {
					out[sub] = true
				}
			}
			return out
		}

		alpha := paths(schemas["v1alpha1"], "")
		beta := paths(schemas["v1beta1"], "")
		Expect(alpha).NotTo(BeEmpty())
		Expect(beta).NotTo(BeEmpty())

		var betaOnly, alphaOnly []string
		for p := range beta {
			if !alpha[p] {
				betaOnly = append(betaOnly, p)
			}
		}
		for p := range alpha {
			if !beta[p] {
				alphaOnly = append(alphaOnly, p)
			}
		}
		Expect(betaOnly).To(BeEmpty(),
			"v1beta1 gained a property v1alpha1 lacks — Task 18's conversion now needs an "+
				"annotation escape hatch, or v1alpha1 must gain the field")
		Expect(alphaOnly).To(ConsistOf("status.phase"),
			"the only property the freeze removes from this kind is status.phase (F2)")
	})

	// ---- F4/F10: the shared ServiceClass, and the priority it also means ----

	// This is the anti-drift check for F10. serviceClass carries a second
	// meaning — the instance's scheduling priority — and the mapping lives in
	// internal/scheduling so the two cannot be edited independently. Read the
	// enum off the *served* CRD rather than from the Go constants, so adding a
	// fourth member without extending the mapping fails here.
	It("maps every serviceClass the schema admits onto a Frame PriorityClass (F10)", func() {
		schemas := servedSchemas()
		enum, found, err := unstructured.NestedSlice(schemas["v1beta1"],
			"properties", "spec", "properties", "serviceClass", "enum")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "spec.serviceClass must be an enum")
		Expect(enum).To(HaveLen(3))

		for _, v := range enum {
			s, ok := v.(string)
			Expect(ok).To(BeTrue())
			Expect(scheduling.PriorityClassForServiceClass(s)).NotTo(BeEmpty(),
				"serviceClass %q has no PriorityClass — the enum and "+
					"internal/scheduling.PriorityClassForServiceClass have drifted (F10)", s)
		}

		// The other half of the mapping's contract: it is total over the enum
		// and empty outside it, so an unclassified value never silently
		// borrows a tier.
		Expect(scheduling.PriorityClassForServiceClass("")).To(BeEmpty())
		Expect(scheduling.PriorityClassForServiceClass("URGENT")).To(BeEmpty())
	})

	// Non-vacuity note: deleting +kubebuilder:default=MEDIUM from *v1beta1*
	// alone does not fail this spec. While conversion is at strategy None,
	// defaulting runs against the storage version's schema too, and v1alpha1
	// carries the same default. Deleting it from both versions does fail it.
	// So this pins MEDIUM as a joint property of the two versions, which is
	// the honest statement while they must agree — and it is deliberately not
	// FrameJob's LOW.
	It("defaults serviceClass to MEDIUM, not FrameJob's LOW (F4)", func() {
		raw := rawSpec("fs-default-serviceclass", map[string]any{"type": "inference"})
		accepts(raw)

		back := &servicesv1beta1.FrameService{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fs-default-serviceclass", Namespace: schemaNS}, back)).To(Succeed())
		Expect(back.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassMedium))
		Expect(back.Spec.DeletionPolicy).To(Equal(policyRetain),
			"a deletionPolicy nobody stated must never destroy data")
	})

	// ---- T8: the type bound ----

	It("accepts a type of exactly 63 characters and rejects 64 (T8)", func() {
		at63 := strings.Repeat("a", 63)
		ok := sampleShaped("fs-t8-type-63")
		ok.Spec.Type = at63
		accepts(ok)

		tooLong := sampleShaped("fs-t8-type-64")
		tooLong.Spec.Type = at63 + "a"
		rejects(tooLong, "type")
	})

	It("still accepts a long, upper-cased type on v1alpha1, so T8 is v1beta1's alone", func() {
		old := &servicesv1alpha1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-t8-alpha-loose-type", Namespace: schemaNS},
			Spec:       servicesv1alpha1.FrameServiceSpec{Type: strings.Repeat("A_b.", 40)},
		}
		accepts(old)
	})

	// ---- T4: the parameters envelope ----

	It("accepts 64 parameters and rejects 65 (T4)", func() {
		params := func(n int) map[string]framev1beta1.ParameterValue {
			out := make(map[string]framev1beta1.ParameterValue, n)
			for i := range n {
				out[fmt.Sprintf("p%d", i)] = "v"
			}
			return out
		}

		ok := sampleShaped("fs-t4-params-64")
		ok.Spec.Parameters = params(64)
		accepts(ok)

		tooMany := sampleShaped("fs-t4-params-65")
		tooMany.Spec.Parameters = params(65)
		rejects(tooMany, "parameters")
	})

	It("accepts a 1024-character parameter value and rejects 1025 (T4)", func() {
		ok := sampleShaped("fs-t4-value-1024")
		ok.Spec.Parameters = map[string]framev1beta1.ParameterValue{
			"prompt": framev1beta1.ParameterValue(strings.Repeat("x", 1024)),
		}
		accepts(ok)

		tooLong := sampleShaped("fs-t4-value-1025")
		tooLong.Spec.Parameters = map[string]framev1beta1.ParameterValue{
			"prompt": framev1beta1.ParameterValue(strings.Repeat("x", 1025)),
		}
		rejects(tooLong, "prompt")
	})

	// The recorded ruling on T4: envelope bounds only, no key pattern.
	// controller-gen silently drops markers on a named key type (no
	// propertyNames is emitted), and the CEL alternative has no key maxLength
	// for the cost estimator to bound against, so it ships a CRD the apiserver
	// refuses to install. Each provider's ParameterSchema validates keys at
	// admission, where they mean something. This spec exists so a later reader
	// finds the decision instead of filing the gap as a bug.
	It("constrains parameter values but deliberately not parameter key form (T4)", func() {
		odd := sampleShaped("fs-t4-odd-keys")
		odd.Spec.Parameters = map[string]framev1beta1.ParameterValue{
			"Mixed_Case.Key":  "accepted",
			"with spaces":     "accepted",
			"UPPER/slash-1":   "accepted",
			"trailing-dash--": "accepted",
		}
		accepts(odd)

		schemas := servedSchemas()
		params, found, err := unstructured.NestedMap(schemas["v1beta1"],
			"properties", "spec", "properties", "parameters")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(params).NotTo(HaveKey("propertyNames"),
			"a key constraint appeared — re-read the T4 ruling before keeping it")
		Expect(params).To(HaveKeyWithValue("maxProperties", int64(64)))
		add, _ := params["additionalProperties"].(map[string]any)
		Expect(add).To(HaveKeyWithValue("maxLength", int64(1024)),
			"the value bound comes from the ParameterValue *value type*; a marker on a named "+
				"key type is silently dropped, and the same marker on the map field itself is "+
				"a hard controller-gen error (\"must apply maxlength to a textual value, "+
				"found type \\\"object\\\"\")")
	})

	It("still accepts an unbounded parameters map on v1alpha1, so T4 is v1beta1's alone", func() {
		params := make(map[string]string, 65)
		for i := range 65 {
			params[fmt.Sprintf("p%d", i)] = strings.Repeat("y", 2000)
		}
		old := &servicesv1alpha1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-t4-alpha-unbounded", Namespace: schemaNS},
			Spec:       servicesv1alpha1.FrameServiceSpec{Type: "inference", Parameters: params},
		}
		accepts(old)
	})

	// ---- binding bounds ----

	It("accepts a 253-character secretName and rejects 254", func() {
		ok := sampleShaped("fs-secretname-253")
		ok.Spec.Binding.SecretName = strings.Repeat("s", 253)
		accepts(ok)

		tooLong := sampleShaped("fs-secretname-254")
		tooLong.Spec.Binding.SecretName = strings.Repeat("s", 254)
		rejects(tooLong, "secretName")
	})

	It("accepts 64 projectTo namespaces and rejects 65", func() {
		nss := func(n int) []string {
			out := make([]string, 0, n)
			for i := range n {
				out = append(out, fmt.Sprintf("team-%d", i))
			}
			return out
		}

		ok := sampleShaped("fs-projectto-64")
		ok.Spec.Binding.ProjectTo = nss(64)
		accepts(ok)

		tooMany := sampleShaped("fs-projectto-65")
		tooMany.Spec.Binding.ProjectTo = nss(65)
		rejects(tooMany, "projectTo")
	})

	It("accepts a 63-character projectTo entry and rejects 64", func() {
		ok := sampleShaped("fs-projectto-63char")
		ok.Spec.Binding.ProjectTo = []string{strings.Repeat("n", 63)}
		accepts(ok)

		tooLong := sampleShaped("fs-projectto-64char")
		tooLong.Spec.Binding.ProjectTo = []string{strings.Repeat("n", 64)}
		rejects(tooLong, "projectTo")
	})

	It("still accepts an unbounded, malformed projectTo on v1alpha1, so the bound is v1beta1's alone", func() {
		old := &servicesv1alpha1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "fs-projectto-alpha-loose", Namespace: schemaNS},
			Spec: servicesv1alpha1.FrameServiceSpec{
				Type:    "inference",
				Binding: servicesv1alpha1.BindingSpec{ProjectTo: []string{strings.Repeat("N", 300)}},
			},
		}
		accepts(old)
	})

	// ---- requiredness ----

	// completeSpec is the whole valid spec as a map; the entries below delete
	// one key from a copy of it. The omission is not reachable through the
	// typed client — Type is a non-pointer string with no omitempty, so a Go
	// client always sends the key with "" and MinLength rejects it first. The
	// mutation that makes this fail is Required -> +optional; stripping
	// Required alone changes no schema byte in this controller-gen, which
	// makes a field required unless it carries +optional.
	completeSpec := func() map[string]any {
		return map[string]any{"type": "inference"}
	}

	It("accepts completeSpec, so the omission below is about the missing key alone", func() {
		accepts(rawSpec("fs-complete-spec", completeSpec()))
	})

	It("rejects a spec with type omitted entirely", func() {
		spec := completeSpec()
		delete(spec, "type")
		rejects(rawSpec("fs-omit-type", spec), "type")
	})

	It("requires both halves of a status.binding.projected coordinate", func() {
		svc := sampleShaped("fs-projected-required")
		accepts(svc)

		key := types.NamespacedName{Name: "fs-projected-required", Namespace: schemaNS}
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(servicesv1beta1.GroupVersion.WithKind("FrameService"))
		Expect(k8sClient.Get(ctx, key, raw)).To(Succeed())
		Expect(unstructured.SetNestedSlice(raw.Object,
			[]any{map[string]any{"namespace": "team-a"}},
			"status", "binding", "projected")).To(Succeed())
		err := k8sClient.Status().Update(ctx, raw)
		Expect(err).To(HaveOccurred(),
			"a projected coordinate with no name would let the controller believe it owns "+
				"every Secret in a namespace")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring("name"))

		// Paired acceptance: the same write with both halves present.
		fresh := &unstructured.Unstructured{}
		fresh.SetGroupVersionKind(servicesv1beta1.GroupVersion.WithKind("FrameService"))
		Expect(k8sClient.Get(ctx, key, fresh)).To(Succeed())
		Expect(unstructured.SetNestedSlice(fresh.Object,
			[]any{map[string]any{"namespace": "team-a", "name": "creds"}},
			"status", "binding", "projected")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, fresh)).To(Succeed())
	})

	// ---- R8: one JSON-tag convention across the frozen API ----

	// omitzero on metadata/status/items would omit a zero-valued envelope
	// where the seven frame.plume-labs.io kinds emit it. The wire difference
	// is small; the point of R8 is that a frozen API states it once. Asserted
	// as a contract between the three types rather than as a snapshot of one,
	// so it fails if any of them drifts.
	It("serialises the same envelope keys as the frame group's kinds (R8)", func() {
		keys := func(v any) []string {
			b, err := json.Marshal(v)
			Expect(err).NotTo(HaveOccurred())
			var m map[string]json.RawMessage
			Expect(json.Unmarshal(b, &m)).To(Succeed())
			out := make([]string, 0, len(m))
			for k := range m {
				out = append(out, k)
			}
			return out
		}

		reference := keys(&framev1beta1.FrameJob{})
		Expect(reference).To(ConsistOf("metadata", "spec", "status"))
		Expect(keys(&servicesv1beta1.FrameService{})).To(ConsistOf(reference))
		Expect(keys(&servicesv1alpha1.FrameService{})).To(ConsistOf(reference))

		listReference := keys(&framev1beta1.FrameJobList{})
		Expect(listReference).To(ConsistOf("metadata", "items"))
		Expect(keys(&servicesv1beta1.FrameServiceList{})).To(ConsistOf(listReference))
		Expect(keys(&servicesv1alpha1.FrameServiceList{})).To(ConsistOf(listReference))
	})

	// ---- the rejection and acceptance tables ----

	DescribeTable("rejects a spec the freeze forbids",
		func(name string, mustMention string, mutate func(*servicesv1beta1.FrameServiceSpec)) {
			svc := sampleShaped(name)
			mutate(&svc.Spec)
			rejects(svc, mustMention)
		},
		// Pinned by the pattern, not by MinLength=1: stripping MinLength alone
		// fails nothing, because "" does not match ^[a-z0-9]... either. The
		// marker is kept for parity with v1alpha1; see the note on Type.
		Entry("an empty type", "fs-reject-empty-type", "type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "" }),
		Entry("an upper-cased type (T8)", "fs-reject-upper-type", "type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "Inference" }),
		Entry("a type with an underscore (T8)", "fs-reject-underscore-type", "type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "in_ference" }),
		Entry("a type with a dot (T8)", "fs-reject-dotted-type", "type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "in.ference" }),
		Entry("a type with a leading dash (T8)", "fs-reject-leading-dash-type", "type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "-inference" }),
		Entry("a type with a trailing dash (T8)", "fs-reject-trailing-dash-type", "type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "inference-" }),
		Entry("a type with a space (T8)", "fs-reject-spaced-type", "type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "in ference" }),
		Entry("a serviceClass outside the enum (F4)", "fs-reject-unknown-class", "serviceClass",
			func(s *servicesv1beta1.FrameServiceSpec) { s.ServiceClass = "URGENT" }),
		Entry("a serviceClass differing only in case (F4)", "fs-reject-cased-class", "serviceClass",
			func(s *servicesv1beta1.FrameServiceSpec) { s.ServiceClass = "Medium" }),
		Entry("a deletionPolicy outside the enum", "fs-reject-unknown-policy", "deletionPolicy",
			func(s *servicesv1beta1.FrameServiceSpec) { s.DeletionPolicy = "Purge" }),
		Entry("a deletionPolicy differing only in case", "fs-reject-cased-policy", "deletionPolicy",
			func(s *servicesv1beta1.FrameServiceSpec) { s.DeletionPolicy = "retain" }),
		Entry("an upper-cased projectTo namespace", "fs-reject-upper-projectto", "projectTo",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Binding.ProjectTo = []string{"Team-A"} }),
		Entry("a projectTo namespace with a slash", "fs-reject-slash-projectto", "projectTo",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Binding.ProjectTo = []string{"team/a"} }),
		Entry("an empty projectTo namespace", "fs-reject-empty-projectto", "projectTo",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Binding.ProjectTo = []string{""} }),
	)

	DescribeTable("accepts every value the schema is meant to allow",
		func(name string, mutate func(*servicesv1beta1.FrameServiceSpec)) {
			svc := sampleShaped(name)
			mutate(&svc.Spec)
			accepts(svc)
		},
		Entry("a one-character type", "fs-accept-tiny-type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "a" }),
		Entry("a dashed, digit-bearing type", "fs-accept-dashed-type",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Type = "llama-3-inference" }),
		Entry("the HIGH tier", "fs-accept-high",
			func(s *servicesv1beta1.FrameServiceSpec) { s.ServiceClass = framev1beta1.ServiceClassHigh }),
		Entry("the MEDIUM tier", "fs-accept-medium",
			func(s *servicesv1beta1.FrameServiceSpec) { s.ServiceClass = framev1beta1.ServiceClassMedium }),
		Entry("the LOW tier", "fs-accept-low",
			func(s *servicesv1beta1.FrameServiceSpec) { s.ServiceClass = framev1beta1.ServiceClassLow }),
		Entry("deletionPolicy Retain", "fs-accept-retain",
			func(s *servicesv1beta1.FrameServiceSpec) { s.DeletionPolicy = policyRetain }),
		Entry("deletionPolicy Delete", "fs-accept-delete",
			func(s *servicesv1beta1.FrameServiceSpec) { s.DeletionPolicy = policyDelete }),
		Entry("no parameters at all", "fs-accept-no-params",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Parameters = nil }),
		Entry("an empty projectTo list", "fs-accept-empty-projectto",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Binding.ProjectTo = []string{} }),
		Entry("a single-character projectTo namespace", "fs-accept-tiny-projectto",
			func(s *servicesv1beta1.FrameServiceSpec) { s.Binding.ProjectTo = []string{"a"} }),
	)
})
