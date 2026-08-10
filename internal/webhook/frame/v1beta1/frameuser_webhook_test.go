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
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func user(name, role string) *framev1beta1.FrameUser {
	return &framev1beta1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cluster-control"},
		Spec: framev1beta1.FrameUserSpec{
			Email: name + "@example.com",
			Role:  role,
		},
	}
}

var _ = Describe("FrameUser webhook", func() {
	newValidator := func(objs ...*framev1beta1.FrameUser) *FrameUserCustomValidator {
		b := fake.NewClientBuilder().WithScheme(scheme.Scheme)
		for _, o := range objs {
			b = b.WithObjects(o)
		}
		return &FrameUserCustomValidator{Client: b.Build()}
	}

	It("refuses deleting the only admin", func() {
		only := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(only, user("bob", framev1beta1.RoleViewer))
		_, err := v.ValidateDelete(context.Background(), only)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows deleting an admin when another remains", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, user("carol", framev1beta1.RoleAdmin))
		_, err := v.ValidateDelete(context.Background(), alice)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses demoting the only admin", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice)
		demoted := alice.DeepCopy()
		demoted.Spec.Role = framev1beta1.RoleViewer
		_, err := v.ValidateUpdate(context.Background(), alice, demoted)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows an admin to keep being an admin", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice)
		same := alice.DeepCopy()
		same.Spec.PasswordAuth = framev1beta1.PasswordEnabled
		_, err := v.ValidateUpdate(context.Background(), alice, same)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows deleting a non-admin even if no admin exists", func() {
		bob := user("bob", framev1beta1.RoleViewer)
		v := newValidator(bob)
		_, err := v.ValidateDelete(context.Background(), bob)
		Expect(err).NotTo(HaveOccurred())
	})

	// The hash guard. Every spec below is written against status.passwordHash
	// because that is what the webhook sees: a v1alpha1 request carrying
	// spec.passwordHash reaches this validator already converted to v1beta1
	// (matchPolicy Equivalent), and conversion.go puts the value on status.
	// conversion_v1alpha1_admission_test.go drives the same guard through a
	// real apiserver at v1alpha1 so that translation is not assumed here.
	Context("the password hash guard", func() {
		const (
			legit    = "argon2id$LEGIT"
			attacker = "argon2id$ATTACKER"
		)

		withStatusSubresource := func() context.Context {
			return admission.NewContextWithRequest(context.Background(),
				admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{SubResource: "status"}})
		}

		It("refuses a create that arrives carrying a hash", func() {
			// The v1alpha1 create vector: spec.passwordHash on a brand-new
			// account. Nothing legitimate does this — authd creates the account
			// and then writes the hash through /status.
			fresh := user("mallory", framev1beta1.RoleAdmin)
			fresh.Status.PasswordHash = attacker
			v := newValidator()
			_, err := v.ValidateCreate(context.Background(), fresh)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("status.passwordHash"))
		})

		It("allows a create with no hash", func() {
			v := newValidator()
			_, err := v.ValidateCreate(context.Background(), user("dave", framev1beta1.RoleViewer))
			Expect(err).NotTo(HaveOccurred())
		})

		It("refuses an update that overwrites the hash", func() {
			alice := user("alice", framev1beta1.RoleAdmin)
			alice.Status.PasswordHash = legit
			v := newValidator(alice)
			attacked := alice.DeepCopy()
			attacked.Status.PasswordHash = attacker
			_, err := v.ValidateUpdate(context.Background(), alice, attacked)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("refusing to change status.passwordHash"))
		})

		It("refuses an update that silently wipes the hash", func() {
			// The other half of C1, and the one that does not look like an
			// attack: a full replace at v1alpha1 that simply omits
			// spec.passwordHash. It needs no /status grant and it fails later,
			// as a 401, rather than here.
			alice := user("alice", framev1beta1.RoleAdmin)
			alice.Status.PasswordHash = legit
			v := newValidator(alice)
			wiped := alice.DeepCopy()
			wiped.Status.PasswordHash = ""
			_, err := v.ValidateUpdate(context.Background(), alice, wiped)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("refusing to clear status.passwordHash"))
		})

		It("allows an update that leaves the hash alone", func() {
			alice := user("alice", framev1beta1.RoleAdmin)
			alice.Status.PasswordHash = legit
			v := newValidator(alice)
			edited := alice.DeepCopy()
			edited.Spec.PasswordAuth = framev1beta1.PasswordEnabled
			_, err := v.ValidateUpdate(context.Background(), alice, edited)
			Expect(err).NotTo(HaveOccurred())
		})

		It("allows the status subresource to change the hash", func() {
			// authd's own write path. The shipped rule does not select
			// frameusers/status, so this branch is what keeps adding it later
			// from breaking the one legitimate writer.
			alice := user("alice", framev1beta1.RoleAdmin)
			alice.Status.PasswordHash = legit
			v := newValidator(alice)
			rotated := alice.DeepCopy()
			rotated.Status.PasswordHash = "argon2id$ROTATED"
			_, err := v.ValidateUpdate(withStatusSubresource(), alice, rotated)
			Expect(err).NotTo(HaveOccurred())
		})

		It("checks the hash before the last-admin rule, so a demotion cannot smuggle one", func() {
			// Ordering matters: the demotion branch returns early on any
			// non-demotion, so a guard placed after it would miss every write
			// that keeps the role.
			alice := user("alice", framev1beta1.RoleAdmin)
			alice.Status.PasswordHash = legit
			v := newValidator(alice, user("carol", framev1beta1.RoleAdmin))
			both := alice.DeepCopy()
			both.Spec.Role = framev1beta1.RoleViewer
			both.Status.PasswordHash = attacker
			_, err := v.ValidateUpdate(context.Background(), alice, both)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("status.passwordHash"))
		})
	})

	It("fails closed when the admin list cannot be read", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		c := fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithObjects(alice).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
					return errors.New("connection refused")
				},
			}).
			Build()
		v := &FrameUserCustomValidator{Client: c}
		_, err := v.ValidateDelete(context.Background(), alice)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("cannot verify remaining admins"))
	})
})
