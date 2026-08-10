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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*. FrameUser has no controller at
// all — authd is its store — so there is no reconciler to test and these live
// in this package only because this is where the envtest suite that serves the
// kustomize-rendered CRDs is.
//
// Two decisions are pinned here.
//
//   - F11: spec.passwordHash is gone from v1beta1 and status.passwordHash has
//     taken its place. Each half is asserted against the other version, so the
//     move is demonstrably a change rather than a restatement.
//   - T6: spec.email gains MaxLength=254, the RFC 5321 limit, because the value
//     becomes the Kubernetes username in every token authd issues.
//
// Nothing here touches the last-admin guard in
// internal/webhook/frame/v1beta1/frameuser_webhook.go. That is admission
// logic, it has its own suite, and no schema rule may duplicate it — this
// suite installs no webhook, which is why these specs can delete freely.
//
// Zero FrameUsers exist on any cluster, verified by `kubectl get frameusers -A`
// before this was written, so the shapes below come from the sample rather
// than from a stored object.
var _ = Describe("FrameUser v1beta1 schema", func() {
	sampleShaped := func(name string) *framev1beta1.FrameUser {
		return &framev1beta1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameUserSpec{
				Email: "admin@example.test",
				Role:  framev1beta1.RoleAdmin,
			},
		}
	}

	rawSpec := func(name string, spec map[string]any) *unstructured.Unstructured {
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameUser"))
		raw.SetNamespace("default")
		raw.SetName(name)
		Expect(unstructured.SetNestedMap(raw.Object, spec, "spec")).To(Succeed())
		return raw
	}

	// A real argon2id PHC string shape. Nothing parses it here; it is only a
	// value whose persistence is being measured.
	const hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"

	// storageVersionAndStrategy reads the served CRD off the running
	// apiserver. Both facts move under later tasks — Task 19 promotes
	// v1beta1, F13/Task 17 turn the conversion strategy to Webhook — and the
	// specs below have to follow them rather than restate today's answer.
	storageVersionAndStrategy := func() (string, string) {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "apiextensions.k8s.io",
			Version: "v1",
			Kind:    "CustomResourceDefinition",
		})
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "frameusers.frame.plume-labs.io"}, crd)).To(Succeed())

		versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		stored := ""
		for _, v := range versions {
			m, ok := v.(map[string]any)
			Expect(ok).To(BeTrue())
			if s, _ := m["storage"].(bool); s {
				stored, _ = m["name"].(string)
			}
		}
		Expect(stored).NotTo(BeEmpty(), "the CRD declares no storage version")

		strategy, _, err := unstructured.NestedString(crd.Object, "spec", "conversion", "strategy")
		Expect(err).NotTo(HaveOccurred())
		if strategy == "" {
			strategy = "None"
		}
		return stored, strategy
	}

	It("holds a sample-shaped object without loss, status included", func() {
		u := sampleShaped("sample-shaped-fu")
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })

		key := types.NamespacedName{Name: "sample-shaped-fu", Namespace: "default"}

		// status.passwordHash is deliberately not written here — whether it
		// persists depends on the storage version, which the dedicated spec
		// below measures rather than assumes.
		u.Status.ObservedGeneration = 1
		u.Status.Credentials = []framev1beta1.WebAuthnCredential{{
			ID:        "Y3JlZC1pZA",
			PublicKey: "cHVia2V5",
			SignCount: 7,
			AddedAt:   metav1.Now(),
			Label:     "YubiKey 5C",
		}}
		Expect(k8sClient.Status().Update(ctx, u)).To(Succeed())

		back := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.Email).To(Equal("admin@example.test"))
		Expect(back.Spec.Role).To(Equal(framev1beta1.RoleAdmin))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 1))
		Expect(back.Status.Credentials).To(HaveLen(1))
		Expect(back.Status.Credentials[0].SignCount).To(BeNumerically("==", 7))
		Expect(back.Status.Credentials[0].Label).To(Equal("YubiKey 5C"))
	})

	// Non-vacuity note: deleting +kubebuilder:default=disabled from *v1beta1
	// alone does not fail this spec. Defaulting, like pruning, also runs
	// against the storage version's schema while conversion is at strategy
	// None, and v1alpha1 carries the same default. Deleting it from both
	// versions does fail it. So this pins the default as a joint property of
	// the two versions — which is the honest statement while they must agree.
	It("defaults passwordAuth to disabled, so an account is passkey-only unless opened", func() {
		u := sampleShaped("fu-default-passwordauth")
		u.Spec.PasswordAuth = ""
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })

		back := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-default-passwordauth", Namespace: "default"}, back)).To(Succeed())
		Expect(back.Spec.PasswordAuth).To(Equal(framev1beta1.PasswordDisabled))
	})

	// ---- F11: the hash moves out of spec ----

	It("drops a spec.passwordHash written on the wire (F11)", func() {
		raw := rawSpec("fu-f11-spec-hash-pruned", map[string]any{
			"email":        "admin@example.test",
			"role":         "admin",
			"passwordHash": hash,
		})
		Expect(k8sClient.Create(ctx, raw)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })

		back := &unstructured.Unstructured{}
		back.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameUser"))
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-f11-spec-hash-pruned", Namespace: "default"}, back)).To(Succeed())

		_, found, err := unstructured.NestedString(back.Object, "spec", "passwordHash")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "v1beta1 must not carry spec.passwordHash (F11)")
	})

	It("still accepts spec.passwordHash on v1alpha1, so the move is v1beta1's alone (F11)", func() {
		// Acceptance, not persistence: v1alpha1 declares the property, so the
		// write is valid there and rejected nowhere. Whether it survives to
		// etcd is the storage-version question, measured in the next spec.
		old := &framev1alpha1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: "fu-f11-alpha-keeps-hash", Namespace: "default"},
			Spec: framev1alpha1.FrameUserSpec{
				Email:        "admin@example.test",
				Role:         framev1alpha1.RoleAdmin,
				PasswordAuth: framev1alpha1.PasswordEnabled,
				PasswordHash: hash,
			},
		}
		Expect(k8sClient.Create(ctx, old)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, old) })
	})

	// The freeze's other kinds make v1beta1 a strict subset of v1alpha1, so
	// storing at either version is lossless and only one direction of the
	// storageversion marker matters. FrameUser is the first kind where the
	// difference runs both ways: v1alpha1 has spec.passwordHash, v1beta1 has
	// status.passwordHash, and neither version declares the other's. With
	// conversion at strategy None the apiserver persists the *intersection* of
	// the request version's schema and the storage version's schema, so
	// exactly one half of the moved field survives — whichever one the storage
	// version happens to declare. Only a serving conversion webhook makes both
	// halves durable, because only then is there a function to map one onto
	// the other.
	//
	// This is measured, not assumed, so that Task 19 (promote v1beta1) and
	// Task 17/F13 (wire the webhook) move the spec's answer rather than break
	// it. If it ever fails, the storage version and the conversion strategy
	// have gone out of step with the code that writes the hash.
	It("persists exactly the half of the moved field that the storage version declares (F11)", func() {
		stored, strategy := storageVersionAndStrategy()

		beta := sampleShaped("fu-f11-persist-beta")
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })
		beta.Status.PasswordHash = hash
		Expect(k8sClient.Status().Update(ctx, beta)).To(Succeed())
		betaBack := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-f11-persist-beta", Namespace: "default"}, betaBack)).To(Succeed())

		alpha := &framev1alpha1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: "fu-f11-persist-alpha", Namespace: "default"},
			Spec: framev1alpha1.FrameUserSpec{
				Email:        "admin@example.test",
				Role:         framev1alpha1.RoleAdmin,
				PasswordAuth: framev1alpha1.PasswordEnabled,
				PasswordHash: hash,
			},
		}
		Expect(k8sClient.Create(ctx, alpha)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, alpha) })
		alphaBack := &framev1alpha1.FrameUser{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-f11-persist-alpha", Namespace: "default"}, alphaBack)).To(Succeed())

		switch {
		case strategy == "Webhook":
			Expect(betaBack.Status.PasswordHash).To(Equal(hash),
				"a serving conversion webhook must make status.passwordHash durable")
			Expect(alphaBack.Spec.PasswordHash).To(Equal(hash),
				"a serving conversion webhook must make spec.passwordHash durable")
		case stored == "v1alpha1":
			Expect(alphaBack.Spec.PasswordHash).To(Equal(hash),
				"v1alpha1 is the storage version, so its half of the field must survive")
			Expect(betaBack.Status.PasswordHash).To(BeEmpty(),
				"status.passwordHash cannot survive while v1alpha1 stores and conversion is None — "+
					"Task 20 must not retype authd onto Status.PasswordHash before the webhook serves")
		case stored == "v1beta1":
			Expect(betaBack.Status.PasswordHash).To(Equal(hash),
				"v1beta1 is the storage version, so its half of the field must survive")
			Expect(alphaBack.Spec.PasswordHash).To(BeEmpty(),
				"spec.passwordHash cannot survive while v1beta1 stores and conversion is None — "+
					"promoting the storage version alone breaks authd while it still speaks v1alpha1")
		default:
			Fail("unexpected storage version " + stored)
		}
	})

	It("has no status.passwordHash on v1alpha1, so the destination is v1beta1's alone (F11)", func() {
		old := &framev1alpha1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: "fu-f11-alpha-no-status-hash", Namespace: "default"},
			Spec: framev1alpha1.FrameUserSpec{
				Email: "admin@example.test",
				Role:  framev1alpha1.RoleAdmin,
			},
		}
		Expect(k8sClient.Create(ctx, old)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, old) })

		// Write the hash into status through the v1alpha1 endpoint, which has
		// no such property. The apiserver prunes it: the field genuinely does
		// not exist on that version, so a v1beta1 -> v1alpha1 round trip must
		// go through spec, which is exactly what Task 18's conversion does.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1alpha1.GroupVersion.WithKind("FrameUser"))
		raw.SetNamespace("default")
		raw.SetName("fu-f11-alpha-no-status-hash")
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-f11-alpha-no-status-hash", Namespace: "default"}, raw)).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, hash, "status", "passwordHash")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, raw)).To(Succeed())

		back := &unstructured.Unstructured{}
		back.SetGroupVersionKind(framev1alpha1.GroupVersion.WithKind("FrameUser"))
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: "fu-f11-alpha-no-status-hash", Namespace: "default"}, back)).To(Succeed())
		_, found, err := unstructured.NestedString(back.Object, "status", "passwordHash")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "v1alpha1 must not carry status.passwordHash (F11)")
	})

	// ---- T6: the email length bound ----

	It("accepts an email of exactly 254 characters and rejects 255 (T6)", func() {
		at254 := strings.Repeat("a", 254-len("@example.test")) + "@example.test"
		Expect(at254).To(HaveLen(254))
		ok := sampleShaped("fu-t6-email-254")
		ok.Spec.Email = at254
		Expect(k8sClient.Create(ctx, ok)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ok) })

		tooLong := sampleShaped("fu-t6-email-255")
		tooLong.Spec.Email = "a" + at254
		err := k8sClient.Create(ctx, tooLong)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tooLong) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted a 255-character email")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring("email"),
			"the rejection must name spec.email, not some other rule")
	})

	It("still accepts an unbounded email on v1alpha1, so the bound is v1beta1's alone (T6)", func() {
		old := &framev1alpha1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: "fu-t6-alpha-long-email", Namespace: "default"},
			Spec: framev1alpha1.FrameUserSpec{
				Email: strings.Repeat("a", 400) + "@example.test",
				Role:  framev1alpha1.RoleViewer,
			},
		}
		Expect(k8sClient.Create(ctx, old)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, old) })
	})

	// ---- requiredness, and the one thing that pins it ----

	// completeSpec is the whole valid spec as a map. The table below deletes
	// one key from a copy of it. Neither omission is reachable through the
	// typed client: email and role are non-pointer strings with no omitempty,
	// so a Go client always sends the key with "" and the pattern or the enum
	// rejects it first. The mutation that makes these specs fail is
	// Required -> +optional; stripping Required alone changes no schema byte
	// in this controller-gen, which makes a field required unless it carries
	// +optional.
	completeSpec := func() map[string]any {
		return map[string]any{
			"email": "admin@example.test",
			"role":  "admin",
		}
	}

	It("accepts completeSpec, so every omission below is about the missing key alone", func() {
		Expect(k8sClient.Create(ctx, rawSpec("complete-spec-fu", completeSpec()))).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, rawSpec("complete-spec-fu", completeSpec()))
		})
	})

	DescribeTable("rejects a spec with a required key omitted entirely",
		func(key string) {
			spec := completeSpec()
			delete(spec, key)
			raw := rawSpec("fu-omit-"+strings.ToLower(key), spec)
			err := k8sClient.Create(ctx, raw)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec with no %s", key)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
			Expect(err.Error()).To(ContainSubstring(key),
				"the rejection must name %s, not some other rule", key)
		},
		Entry("email", "email"),
		Entry("role", "role"),
	)

	It("rejects an object with no spec at all, and v1alpha1 accepts one", func() {
		// Spec is `+required` on v1beta1 where v1alpha1 had `spec,omitempty`.
		// A FrameUser with no spec has neither an email nor a role, so this is
		// a correctness fix rather than a new constraint — but it *is* a
		// difference between the versions, so both halves are asserted.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameUser"))
		raw.SetNamespace("default")
		raw.SetName("fu-no-spec-beta")
		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), "the apiserver accepted a v1beta1 FrameUser with no spec")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		Expect(err.Error()).To(ContainSubstring("spec"))

		oldRaw := &unstructured.Unstructured{}
		oldRaw.SetGroupVersionKind(framev1alpha1.GroupVersion.WithKind("FrameUser"))
		oldRaw.SetNamespace("default")
		oldRaw.SetName("fu-no-spec-alpha")
		Expect(k8sClient.Create(ctx, oldRaw)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, oldRaw) })
	})

	DescribeTable("rejects a spec the freeze forbids",
		func(name string, mutate func(*framev1beta1.FrameUserSpec)) {
			u := sampleShaped(name)
			mutate(&u.Spec)
			err := k8sClient.Create(ctx, u)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec the schema must reject")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		},
		Entry("an email longer than 254 characters (T6)", "fu-reject-long-email",
			func(s *framev1beta1.FrameUserSpec) {
				s.Email = strings.Repeat("a", 242) + "@example.test"
			}),
		Entry("an empty email", "fu-reject-empty-email",
			func(s *framev1beta1.FrameUserSpec) { s.Email = "" }),
		Entry("an email with no at-sign", "fu-reject-atless-email",
			func(s *framev1beta1.FrameUserSpec) { s.Email = "admin.example.test" }),
		Entry("an email with two at-signs", "fu-reject-double-at-email",
			func(s *framev1beta1.FrameUserSpec) { s.Email = "ad@min@example.test" }),
		Entry("an email containing a space", "fu-reject-spaced-email",
			func(s *framev1beta1.FrameUserSpec) { s.Email = "ad min@example.test" }),
		Entry("an empty role", "fu-reject-empty-role",
			func(s *framev1beta1.FrameUserSpec) { s.Role = "" }),
		Entry("a role outside the enum", "fu-reject-unknown-role",
			func(s *framev1beta1.FrameUserSpec) { s.Role = "superadmin" }),
		Entry("a role differing only in case", "fu-reject-cased-role",
			func(s *framev1beta1.FrameUserSpec) { s.Role = "Admin" }),
		Entry("a passwordAuth outside the enum", "fu-reject-unknown-passwordauth",
			func(s *framev1beta1.FrameUserSpec) { s.PasswordAuth = "maybe" }),
	)

	DescribeTable("accepts every value the schema is meant to allow",
		func(name string, mutate func(*framev1beta1.FrameUserSpec)) {
			u := sampleShaped(name)
			mutate(&u.Spec)
			Expect(k8sClient.Create(ctx, u)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
		},
		Entry("the admin role", "fu-accept-admin",
			func(s *framev1beta1.FrameUserSpec) { s.Role = framev1beta1.RoleAdmin }),
		Entry("the operator role", "fu-accept-operator",
			func(s *framev1beta1.FrameUserSpec) { s.Role = framev1beta1.RoleOperator }),
		Entry("the viewer role", "fu-accept-viewer",
			func(s *framev1beta1.FrameUserSpec) { s.Role = framev1beta1.RoleViewer }),
		Entry("passwordAuth enabled", "fu-accept-password-enabled",
			func(s *framev1beta1.FrameUserSpec) { s.PasswordAuth = framev1beta1.PasswordEnabled }),
		Entry("passwordAuth disabled", "fu-accept-password-disabled",
			func(s *framev1beta1.FrameUserSpec) { s.PasswordAuth = framev1beta1.PasswordDisabled }),
		Entry("a three-character email", "fu-accept-tiny-email",
			func(s *framev1beta1.FrameUserSpec) { s.Email = "a@b" }),
		Entry("an email with a plus tag and sub-domains", "fu-accept-plus-tag-email",
			func(s *framev1beta1.FrameUserSpec) { s.Email = "ada+frame@mail.corp.example.test" }),
		// The pattern is deliberately loose. This is not a well-formed address
		// under RFC 5322, and it is accepted on purpose: the recorded decision
		// is that a CRD should not try to enforce that grammar.
		Entry("a loose address the pattern deliberately allows", "fu-accept-loose-email",
			func(s *framev1beta1.FrameUserSpec) { s.Email = `"quoted"@[::1]` }),
	)
})
