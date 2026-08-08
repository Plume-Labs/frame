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

package v1alpha1

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

func user(name, role string) *framev1alpha1.FrameUser {
	return &framev1alpha1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cluster-control"},
		Spec: framev1alpha1.FrameUserSpec{
			Email: name + "@example.com",
			Role:  role,
		},
	}
}

var _ = Describe("FrameUser webhook", func() {
	newValidator := func(objs ...*framev1alpha1.FrameUser) *FrameUserCustomValidator {
		b := fake.NewClientBuilder().WithScheme(scheme.Scheme)
		for _, o := range objs {
			b = b.WithObjects(o)
		}
		return &FrameUserCustomValidator{Client: b.Build()}
	}

	It("refuses deleting the only admin", func() {
		only := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(only, user("bob", framev1alpha1.RoleViewer))
		_, err := v.ValidateDelete(context.Background(), only)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows deleting an admin when another remains", func() {
		alice := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(alice, user("carol", framev1alpha1.RoleAdmin))
		_, err := v.ValidateDelete(context.Background(), alice)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses demoting the only admin", func() {
		alice := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(alice)
		demoted := alice.DeepCopy()
		demoted.Spec.Role = framev1alpha1.RoleViewer
		_, err := v.ValidateUpdate(context.Background(), alice, demoted)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows an admin to keep being an admin", func() {
		alice := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(alice)
		same := alice.DeepCopy()
		same.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
		_, err := v.ValidateUpdate(context.Background(), alice, same)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows deleting a non-admin even if no admin exists", func() {
		bob := user("bob", framev1alpha1.RoleViewer)
		v := newValidator(bob)
		_, err := v.ValidateDelete(context.Background(), bob)
		Expect(err).NotTo(HaveOccurred())
	})

	It("fails closed when the admin list cannot be read", func() {
		alice := user("alice", framev1alpha1.RoleAdmin)
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
