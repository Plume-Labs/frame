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

package v1beta1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// This file is the only place in the repository where a v1alpha1 admission
// request is exercised, and it exists because the unit specs next door cannot
// reach the thing that was actually broken.
//
// F11 moved the password hash onto status so that `patch frameusers` could no
// longer set anyone's password. That holds at v1beta1 and only at v1beta1.
// RBAC has no version dimension, CR schema validation runs against the request
// version, and conversion output is stored without re-validation — so
// v1alpha1's spec.passwordHash was a write channel straight into v1beta1's
// status.passwordHash, needing nothing but the editor tier's existing grant.
// Nothing about that is visible from Go: it depends on the apiserver
// converting the request (matchPolicy Equivalent), on the /convert webhook
// putting the value on status, and on the guard being consulted for a version
// the webhook rule does not name.
//
// So every spec here writes at v1alpha1 through the apiserver and asserts on
// what v1beta1 holds afterwards. The suite registers v1alpha1 in the scheme
// for exactly this; see the note in webhook_suite_test.go.
var _ = Describe("the v1alpha1 write channel into status.passwordHash", func() {
	const legit = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$bGVnaXQ"
	const attacker = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$YXR0YWNrZXI"

	var name string
	var seq int

	// seed creates the account at v1beta1 and gives it a hash the way authd
	// does — through the status subresource — then hands back the stored
	// v1beta1 object. That the seeding works at all is half the point: the
	// guard must not touch the one legitimate writer.
	seed := func() *framev1beta1.FrameUser {
		GinkgoHelper()
		seq++
		name = "hash-guard-" + string(rune('a'+seq-1))

		beta := &framev1beta1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameUserSpec{
				Email:        name + "@frame.test",
				Role:         framev1beta1.RoleOperator,
				PasswordAuth: framev1beta1.PasswordEnabled,
			},
		}
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })

		beta.Status.PasswordHash = legit
		Expect(k8sClient.Status().Update(ctx, beta)).To(Succeed(),
			"authd's own write path must stay open, or nobody can ever set a password")

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(beta), beta)).To(Succeed())
		Expect(beta.Status.PasswordHash).To(Equal(legit))
		return beta
	}

	storedHash := func() string {
		GinkgoHelper()
		beta := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, beta)).To(Succeed())
		return beta.Status.PasswordHash
	}

	// alphaView reads the same object at the deprecated version, where the
	// hash is spelled spec.passwordHash.
	alphaView := func() *framev1alpha1.FrameUser {
		GinkgoHelper()
		alpha := &framev1alpha1.FrameUser{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, alpha)).To(Succeed())
		return alpha
	}

	It("refuses a v1alpha1 merge patch that overwrites the hash", func() {
		// The reported attack, verbatim: one merge patch of
		// spec.passwordHash, needing nothing beyond `patch frameusers`.
		seed()
		alpha := alphaView()
		Expect(alpha.Spec.PasswordHash).To(Equal(legit),
			"conversion must be serving, or this spec proves nothing")

		patched := alpha.DeepCopy()
		patched.Spec.PasswordHash = attacker
		err := k8sClient.Patch(ctx, patched, client.MergeFrom(alpha))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("refusing to change status.passwordHash"))
		Expect(storedHash()).To(Equal(legit))
	})

	It("refuses a v1alpha1 replace that omits the hash, which would wipe it", func() {
		// Not an attack — an ordinary `kubectl replace` or a client that
		// round-trips a spec it does not know every field of. Conversion
		// carries the omission through faithfully and the credential is gone,
		// with a 200 and a 401 some time later.
		seed()
		alpha := alphaView()

		alpha.Spec.PasswordHash = ""
		err := k8sClient.Update(ctx, alpha)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("refusing to clear status.passwordHash"))
		Expect(storedHash()).To(Equal(legit))
	})

	It("refuses a v1alpha1 create that arrives carrying a hash", func() {
		// Create is the third vector: an account nobody else knows the
		// password of, which the editor tier can also give role: admin.
		mallory := &framev1alpha1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: "hash-guard-create", Namespace: "default"},
			Spec: framev1alpha1.FrameUserSpec{
				Email:        "mallory@frame.test",
				Role:         framev1alpha1.RoleAdmin,
				PasswordAuth: framev1alpha1.PasswordEnabled,
				PasswordHash: attacker,
			},
		}
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, mallory) })

		err := k8sClient.Create(ctx, mallory)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("status.passwordHash"))
	})

	It("still lets a v1alpha1 client edit everything else", func() {
		// The guard has to be narrow or it breaks the deprecation window it
		// protects. A merge patch that does not mention the hash leaves it in
		// place — the apiserver applies the patch to the converted view, where
		// the field is present — so an ordinary v1alpha1 edit still works.
		seed()
		alpha := alphaView()

		patched := alpha.DeepCopy()
		patched.Spec.PasswordAuth = framev1alpha1.PasswordDisabled
		Expect(k8sClient.Patch(ctx, patched, client.MergeFrom(alpha))).To(Succeed())
		Expect(storedHash()).To(Equal(legit))
	})

	It("still lets a v1alpha1 replace through when it carries the stored hash", func() {
		// The same full PUT as the wipe spec, with the field kept. This is the
		// discriminator between "the guard rejects replaces" (wrong) and "the
		// guard rejects hash changes" (what it does).
		seed()
		alpha := alphaView()

		alpha.Spec.Email = "renamed@frame.test"
		Expect(k8sClient.Update(ctx, alpha)).To(Succeed())
		Expect(storedHash()).To(Equal(legit))
	})

	It("still lets authd rotate the hash through the v1beta1 status subresource", func() {
		// The one legitimate writer, after the guard is in place.
		beta := seed()
		beta.Status.PasswordHash = attacker
		Expect(k8sClient.Status().Update(ctx, beta)).To(Succeed())
		Expect(storedHash()).To(Equal(attacker))
	})
})
