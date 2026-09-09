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
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// user leaves Spec.State unset ("") on purpose, standing in for an account
// written before spec.state existed. That omission is load-bearing, not an
// oversight: "allows disabling an admin when another enabled admin remains"
// depends on it on both sides at once — the subject alice, whose unset old
// state must itself read as enabled for the admin-disable branch in
// ValidateUpdate to fire at all, and the counting side carol, whose unset
// state must count as enabled inside requireAnotherAdmin's loop for the
// write to be permitted. If this fixture starts setting
// State: framev1beta1.StateEnabled explicitly, that coverage of the "" case
// disappears without turning the suite red, and isEnabled could regress to
// `state == StateEnabled` unnoticed.
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
	// requestBy builds the admission request the apiserver would send, naming
	// who is making the write. Under impersonation those groups are the
	// impersonated user's, which is what makes this guard line up with the
	// tiers rather than with the proxy's ServiceAccount.
	requestBy := func(groups ...string) context.Context {
		return admission.NewContextWithRequest(context.Background(),
			admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				UserInfo: authenticationv1.UserInfo{Username: "someone@example.com", Groups: groups},
			}})
	}

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
		// An admin requester, so this exercises the last-admin rule rather
		// than the "who may delete an admin" guard in front of it.
		_, err := v.ValidateDelete(requestBy("frame:admins"), only)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows deleting an admin when another remains", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, user("carol", framev1beta1.RoleAdmin))
		_, err := v.ValidateDelete(requestBy("frame:admins"), alice)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses demoting the only admin", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice)
		demoted := alice.DeepCopy()
		demoted.Spec.Role = framev1beta1.RoleViewer
		// An admin requester, so this exercises the last-admin rule rather
		// than the role-change authorization one in front of it.
		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, demoted)
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

	// C4 of the whole-branch review, second half. The first half is the
	// aggregation (test/manifests): the tier labels put frameuser-editor-role
	// in the editor tier, so an operator held patch on frameusers. This is
	// the half that holds even if someone re-adds the label, binds the role
	// by hand, or reaches the object with a kubeconfig — a role change has to
	// be made by somebody who is already an admin.
	//
	// Both halves are needed and neither is sufficient: RBAC decides who may
	// send the request, admission decides what the request may say.
	Context("who may change a role", func() {
		It("refuses an operator promoting themselves to admin", func() {
			bob := user("bob", framev1beta1.RoleOperator)
			v := newValidator(bob, user("alice", framev1beta1.RoleAdmin))
			promoted := bob.DeepCopy()
			promoted.Spec.Role = framev1beta1.RoleAdmin
			_, err := v.ValidateUpdate(requestBy("frame:operators", "system:authenticated"), bob, promoted)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.role"))
		})

		It("refuses an operator demoting an admin", func() {
			// The mirror: locking the admins out is as much a privilege
			// escalation as promoting yourself.
			alice := user("alice", framev1beta1.RoleAdmin)
			v := newValidator(alice, user("carol", framev1beta1.RoleAdmin))
			demoted := alice.DeepCopy()
			demoted.Spec.Role = framev1beta1.RoleViewer
			_, err := v.ValidateUpdate(requestBy("frame:operators"), alice, demoted)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.role"))
		})

		It("allows an admin to change a role", func() {
			bob := user("bob", framev1beta1.RoleViewer)
			v := newValidator(bob, user("alice", framev1beta1.RoleAdmin))
			promoted := bob.DeepCopy()
			promoted.Spec.Role = framev1beta1.RoleOperator
			_, err := v.ValidateUpdate(requestBy("frame:admins"), bob, promoted)
			Expect(err).NotTo(HaveOccurred())
		})

		It("allows a break-glass cluster-admin", func() {
			// The node's own kubeconfig is the documented way back from a
			// stranded rollout (docs/deployment.md). Refusing it would make
			// this guard the thing that makes the lockout permanent.
			bob := user("bob", framev1beta1.RoleViewer)
			v := newValidator(bob)
			promoted := bob.DeepCopy()
			promoted.Spec.Role = framev1beta1.RoleAdmin
			_, err := v.ValidateUpdate(requestBy("system:masters"), bob, promoted)
			Expect(err).NotTo(HaveOccurred())
		})

		It("refuses a role change it cannot attribute", func() {
			// Fail closed. An admission request with no UserInfo is not
			// evidence that the caller is an admin.
			bob := user("bob", framev1beta1.RoleViewer)
			v := newValidator(bob, user("alice", framev1beta1.RoleAdmin))
			promoted := bob.DeepCopy()
			promoted.Spec.Role = framev1beta1.RoleAdmin
			_, err := v.ValidateUpdate(context.Background(), bob, promoted)
			Expect(err).To(HaveOccurred())
		})

		It("leaves a write that does not touch the role alone", func() {
			// The guard must fire on role changes only: an operator editing
			// their own display fields is an ordinary write, and if this
			// needed an admin the console would refuse every self-service
			// edit.
			bob := user("bob", framev1beta1.RoleOperator)
			v := newValidator(bob, user("alice", framev1beta1.RoleAdmin))
			edited := bob.DeepCopy()
			edited.Spec.PasswordAuth = framev1beta1.PasswordEnabled
			_, err := v.ValidateUpdate(requestBy("frame:operators"), bob, edited)
			Expect(err).NotTo(HaveOccurred())
		})

		// The other half of C4: frameuser-editor-role grants create as well
		// as update, so a principal holding it could sidestep every check
		// above by creating a brand-new admin FrameUser instead of patching
		// an existing one. Run against the code before this fix (comment out
		// the anyAdmin/requireAdminRequester block in ValidateCreate), every
		// one of these first three cases went the wrong way: an operator's
		// create with spec.role: admin succeeded instead of failing, and the
		// unattributed create succeeded too — only guardPasswordHash ran.
		It("refuses an operator creating a second admin at their own email", func() {
			v := newValidator(user("alice", framev1beta1.RoleAdmin))
			mallory := user("mallory", framev1beta1.RoleAdmin)
			_, err := v.ValidateCreate(requestBy("frame:operators"), mallory)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.role"))
		})

		It("refuses a create of an admin it cannot attribute, once an admin exists", func() {
			// Fail closed, the same way an unattributed role change does.
			v := newValidator(user("alice", framev1beta1.RoleAdmin))
			mallory := user("mallory", framev1beta1.RoleAdmin)
			_, err := v.ValidateCreate(context.Background(), mallory)
			Expect(err).To(HaveOccurred())
		})

		It("allows an admin to create another admin", func() {
			v := newValidator(user("alice", framev1beta1.RoleAdmin))
			dave := user("dave", framev1beta1.RoleAdmin)
			_, err := v.ValidateCreate(requestBy("frame:admins"), dave)
			Expect(err).NotTo(HaveOccurred())
		})

		It("allows the very first admin to be created with no admin requester", func() {
			// Bootstrap: authd's own AdminCount() == 0 check is what
			// authorizes this create (server_bootstrap.go), and the request
			// itself carries no frame:admins group because there is no admin
			// yet to belong to. A guard that refused this would break
			// /auth/bootstrap on every fresh install.
			v := newValidator()
			first := user("alice", framev1beta1.RoleAdmin)
			_, err := v.ValidateCreate(context.Background(), first)
			Expect(err).NotTo(HaveOccurred())
		})

		It("leaves a create of a non-admin alone", func() {
			v := newValidator(user("alice", framev1beta1.RoleAdmin))
			_, err := v.ValidateCreate(requestBy("frame:operators"), user("bob", framev1beta1.RoleViewer))
			Expect(err).NotTo(HaveOccurred())
		})

		// Delete-then-recreate is the path the create guard above already
		// closes on its own (a recreate with spec.role: admin is a create),
		// but an unguarded delete is its own privilege-affecting write: it
		// can remove an admin who is not the last one using nothing but
		// `delete`, without ever touching spec.role. Run against the code
		// before this fix (the ValidateDelete below with no
		// requireAdminRequester call), the first case succeeded instead of
		// failing: an operator could delete an admin outright as long as a
		// second admin remained to satisfy the last-admin check.
		It("refuses an operator deleting an admin who is not the last one", func() {
			alice := user("alice", framev1beta1.RoleAdmin)
			carol := user("carol", framev1beta1.RoleAdmin)
			v := newValidator(alice, carol)
			_, err := v.ValidateDelete(requestBy("frame:operators"), alice)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("delete admin FrameUser"))
		})

		It("refuses a delete of an admin it cannot attribute", func() {
			alice := user("alice", framev1beta1.RoleAdmin)
			carol := user("carol", framev1beta1.RoleAdmin)
			v := newValidator(alice, carol)
			_, err := v.ValidateDelete(context.Background(), alice)
			Expect(err).To(HaveOccurred())
		})

		It("allows an admin to delete another admin", func() {
			alice := user("alice", framev1beta1.RoleAdmin)
			carol := user("carol", framev1beta1.RoleAdmin)
			v := newValidator(alice, carol)
			_, err := v.ValidateDelete(requestBy("frame:admins"), alice)
			Expect(err).NotTo(HaveOccurred())
		})

		It("leaves deleting a non-admin alone, even for a non-admin requester", func() {
			bob := user("bob", framev1beta1.RoleViewer)
			v := newValidator(bob, user("alice", framev1beta1.RoleAdmin))
			_, err := v.ValidateDelete(requestBy("frame:operators"), bob)
			Expect(err).NotTo(HaveOccurred())
		})
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
		// An admin requester, so this reaches the last-admin list read
		// instead of failing earlier on the "who may delete an admin" guard.
		_, err := v.ValidateDelete(requestBy("frame:admins"), alice)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("cannot verify remaining admins"))
	})

	// disabledUser is `user` switched off. The helper exists so a spec reads
	// as a sentence rather than as two statements.
	disabledUser := func(name, role string) *framev1beta1.FrameUser {
		u := user(name, role)
		u.Spec.State = framev1beta1.StateDisabled
		return u
	}

	It("refuses a non-admin switching an account off", func() {
		alice := user("alice", framev1beta1.RoleViewer)
		v := newValidator(alice, user("root", framev1beta1.RoleAdmin))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		_, err := v.ValidateUpdate(requestBy("frame:viewers"), alice, off)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.state"))
		Expect(err.Error()).To(ContainSubstring("not an admin"))
	})

	It("lets an admin switch an account off and back on", func() {
		alice := user("alice", framev1beta1.RoleViewer)
		v := newValidator(alice, user("root", framev1beta1.RoleAdmin))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).NotTo(HaveOccurred())

		on := off.DeepCopy()
		on.Spec.State = framev1beta1.StateEnabled
		_, err = v.ValidateUpdate(requestBy("frame:admins"), off, on)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses disabling the only admin", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, user("bob", framev1beta1.RoleViewer))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		// An admin requester, so this exercises the last-admin rule rather
		// than the authorization one in front of it.
		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows disabling an admin when another enabled admin remains", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, user("carol", framev1beta1.RoleAdmin))
		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled

		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).NotTo(HaveOccurred())
	})

	// The discriminating one. A disabled admin cannot obtain a token — authd
	// refuses every identity-issuing path for it — so counting it as "another
	// admin" would let the last usable admin be demoted, deleted or disabled
	// behind an account nobody can sign in to. Before requireAnotherAdmin
	// looked at spec.state, this passed and locked the cluster out of its own
	// console.
	It("does not count a disabled admin as the admin who remains", func() {
		alice := user("alice", framev1beta1.RoleAdmin)
		v := newValidator(alice, disabledUser("dave", framev1beta1.RoleAdmin))

		demoted := alice.DeepCopy()
		demoted.Spec.Role = framev1beta1.RoleViewer
		_, err := v.ValidateUpdate(requestBy("frame:admins"), alice, demoted)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))

		_, err = v.ValidateDelete(requestBy("frame:admins"), alice)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))

		off := alice.DeepCopy()
		off.Spec.State = framev1beta1.StateDisabled
		_, err = v.ValidateUpdate(requestBy("frame:admins"), alice, off)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	// A create cannot take anyone's access away, and refusing one would break
	// /auth/invite, which creates under authd's ServiceAccount. Pinned so the
	// asymmetry is a decision rather than an omission.
	It("does not guard spec.state on create", func() {
		v := newValidator(user("root", framev1beta1.RoleAdmin))
		_, err := v.ValidateCreate(requestBy("frame:viewers"), disabledUser("newbie", framev1beta1.RoleViewer))
		Expect(err).NotTo(HaveOccurred())
	})

	// Pins the admin-disable last-admin branch to oldObj.Spec.Role, not
	// newObj.Spec.Role. A promotion combined with a disable in the same write
	// (viewer/enabled -> admin/disabled) removes nobody from the admin pool —
	// alice was never an admin whose access this write could be taking away —
	// so it must not be refused by the rule that exists to stop the last
	// admin from disappearing. Keying on newObj.Spec.Role instead would treat
	// alice as the admin being removed and refuse this with no other enabled
	// admin around, which is why this write goes through system:masters
	// rather than an existing frame:admins member: it is meant to exercise
	// the break-glass path a wrongly-refused bootstrap write would strand.
	It("allows promoting and disabling an account in the same write", func() {
		alice := user("alice", framev1beta1.RoleViewer)
		v := newValidator(alice)
		promotedAndOff := alice.DeepCopy()
		promotedAndOff.Spec.Role = framev1beta1.RoleAdmin
		promotedAndOff.Spec.State = framev1beta1.StateDisabled

		_, err := v.ValidateUpdate(requestBy(clusterAdminGroup), alice, promotedAndOff)
		Expect(err).NotTo(HaveOccurred())
	})
})
