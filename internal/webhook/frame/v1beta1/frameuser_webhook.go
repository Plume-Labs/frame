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
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameusers,verbs=get;list;watch

// SetupFrameUserWebhookWithManager registers the webhook for FrameUser.
func SetupFrameUserWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1beta1.FrameUser{}).
		WithValidator(&FrameUserCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// matchPolicy=Equivalent is load-bearing here, not decoration, and it is the
// one webhook in the tree that says so explicitly. Equivalent is the apiserver
// default, so the other nine rely on it implicitly; this one *depends* on it.
// The rule below selects versions=v1beta1, and the password-hash guard exists
// to stop a write arriving at v1alpha1. Only Equivalent makes the apiserver
// convert that request to v1beta1 and dispatch it here; under Exact the
// v1alpha1 write would skip admission entirely and the guard would be a
// no-op — silently, with every test still green.
//
// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1beta1-frameuser,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,sideEffects=None,groups=frame.plume-labs.io,resources=frameusers,verbs=create;update;delete,versions=v1beta1,name=vframeuser-v1beta1.kb.io,admissionReviewVersions=v1

// FrameUserCustomValidator keeps at least one admin in existence, keeps
// status.passwordHash off every write path except the status subresource, and
// keeps spec.role changes in the hands of admins.
//
// This lives at admission rather than inside authd because authd is not the
// only writer: admins create, delete and re-role accounts straight through the
// apiserver under their own identity, and kubectl bypasses authd entirely.
// Admission is the only chokepoint every write passes through.
type FrameUserCustomValidator struct {
	Client client.Client
}

func (v *FrameUserCustomValidator) ValidateCreate(ctx context.Context, obj *framev1beta1.FrameUser) (admission.Warnings, error) {
	// A create carries no old object, so any hash on it is a hash the creator
	// chose. At v1beta1 the apiserver has already cleared status by the time
	// admission runs (PrepareForCreate precedes validating admission), so this
	// can only be non-empty on a v1alpha1 create carrying spec.passwordHash.
	return nil, guardPasswordHash(ctx, "", obj.Status.PasswordHash)
}

func (v *FrameUserCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *framev1beta1.FrameUser) (admission.Warnings, error) {
	if err := guardPasswordHash(ctx, oldObj.Status.PasswordHash, newObj.Status.PasswordHash); err != nil {
		return nil, err
	}
	// Before the last-admin rule, not after it: whether another admin exists
	// is not something to tell a caller who has no business changing a role
	// in the first place.
	if oldObj.Spec.Role != newObj.Spec.Role {
		if err := requireAdminRequester(ctx, oldObj.Spec.Role, newObj.Spec.Role); err != nil {
			return nil, err
		}
	}
	// Only a demotion can remove an admin; anything else leaves the count alone.
	if oldObj.Spec.Role != framev1beta1.RoleAdmin || newObj.Spec.Role == framev1beta1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, oldObj.Name)
}

// Groups that count as "already an admin" for the purpose of changing a role.
//
// adminGroup is the group the uiproxy impersonates a FrameUser with
// spec.role: admin into (GroupForRole in internal/authd/issuer.go, plus the
// `frame:` prefix the proxy applies). Under impersonation the apiserver puts
// the *impersonated* identity in the AdmissionReview, so this is the caller
// as the console knows them.
//
// clusterAdminGroup is the break-glass path. The node's own kubeconfig is
// what docs/deployment.md sends an operator to when a rollout strands the
// first admin, and it is the only way back from an empty FrameUser list —
// where, by definition, no frame:admins member exists to authorize anything.
// A guard that refused it would be the thing that made a lockout permanent.
const (
	adminGroup        = "frame:admins"
	clusterAdminGroup = "system:masters"
)

// requireAdminRequester refuses a change to spec.role made by anyone who is
// not already an admin.
//
// This is the second half of the fix for the promotion path the whole-branch
// review found (C4). The first half is RBAC: frameuser-editor-role is no
// longer in the editor tier, so an operator cannot send the request at all.
// This half is what holds when RBAC is wrong — a re-added tier label, a
// hand-made binding, a kubeconfig issued to the wrong person. Without it the
// only rule on this field was the last-admin guard below, which returns nil
// on its first clause for every write that is not a demotion: a self-promotion
// to admin was explicitly allowed, and one token lifetime later the account
// carried frame:admins.
//
// Fails closed. An admission request with no UserInfo, or no request in the
// context at all, is not evidence that the caller is an admin.
func requireAdminRequester(ctx context.Context, oldRole, newRole string) error {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return fmt.Errorf("refusing to change spec.role from %q to %q: cannot identify who is making the request (%w)",
			oldRole, newRole, err)
	}
	for _, g := range req.UserInfo.Groups {
		if g == adminGroup || g == clusterAdminGroup {
			return nil
		}
	}
	return fmt.Errorf(
		"refusing to change spec.role from %q to %q: %q is not an admin. "+
			"Only a member of %s (a FrameUser with spec.role: admin) or %s may change a role — "+
			"otherwise anyone who can patch a FrameUser can make themselves an admin, "+
			"and anyone who can patch one can lock the admins out",
		oldRole, newRole, req.UserInfo.Username, adminGroup, clusterAdminGroup)
}

func (v *FrameUserCustomValidator) ValidateDelete(ctx context.Context, obj *framev1beta1.FrameUser) (admission.Warnings, error) {
	if obj.Spec.Role != framev1beta1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, obj.Name)
}

// guardPasswordHash is what makes the F11 status split real while v1alpha1 is
// still served.
//
// Moving the hash onto status bought write protection *at v1beta1 only*. RBAC
// has no version dimension — `patch frameusers` covers every served version —
// and CR schema validation runs against the request version while conversion
// output is stored without re-validation. So v1alpha1's spec.passwordHash was
// an unguarded write channel straight into v1beta1's status.passwordHash: a
// principal holding nothing but `patch frameusers` could set any account's
// password, and the documented `/status` route is inert at v1alpha1 (the
// status strategy reverts spec, and v1alpha1 has no status.passwordHash at
// all). Admission is the only chokepoint that covers both versions, because
// matchPolicy Equivalent converts the v1alpha1 request up to v1beta1 first.
//
// Two distinct failures, one rule:
//
//   - overwrite — a request that sets a hash different from the stored one;
//   - silent wipe — a full PUT at v1alpha1 that simply omits
//     spec.passwordHash. Conversion faithfully carries the omission through as
//     an empty status.passwordHash, destroying the credential with a 200 and
//     surfacing later as a 401 rather than as an error. No /status grant is
//     needed for it, and nothing about the request looks hostile.
//
// Comparing old against new catches both, and it lets every write that leaves
// the hash alone through untouched — which is every legitimate main-resource
// write, at either version.
func guardPasswordHash(ctx context.Context, oldHash, newHash string) error {
	if newHash == oldHash {
		return nil
	}

	// The webhook rule deliberately selects `frameusers` and not
	// `frameusers/status`, so today every request reaching this function is a
	// main-resource write. The check is here so that stays true by decision
	// rather than by accident: adding the subresource to the rule later — to
	// run the last-admin guard on status writes, say — must not break authd,
	// which is the field's one legitimate writer.
	if req, err := admission.RequestFromContext(ctx); err == nil && req.SubResource == "status" {
		return nil
	}

	if newHash == "" {
		return fmt.Errorf(
			"refusing to clear status.passwordHash: this write does not carry the stored hash, " +
				"which destroys the account's password with a success response " +
				"(a full replace at v1alpha1 that omits spec.passwordHash does exactly this). " +
				"Patch the field instead of replacing the object, or write it through the " +
				"v1beta1 frameusers/status subresource")
	}
	return fmt.Errorf(
		"refusing to change status.passwordHash on the main resource: it is credential material " +
			"and only the v1beta1 frameusers/status subresource may write it (F11). " +
			"Writing spec.passwordHash at the deprecated v1alpha1 version is the same write and is " +
			"refused for the same reason")
}

// requireAnotherAdmin fails unless some admin other than `excluding` exists.
func (v *FrameUserCustomValidator) requireAnotherAdmin(ctx context.Context, excluding string) error {
	var users framev1beta1.FrameUserList
	if err := v.Client.List(ctx, &users); err != nil {
		// Fail closed: an unreadable list is not evidence that another admin
		// exists, and guessing wrong here locks everyone out of the UI.
		return fmt.Errorf("cannot verify remaining admins: %w", err)
	}
	for _, u := range users.Items {
		if u.Name != excluding && u.Spec.Role == framev1beta1.RoleAdmin {
			return nil
		}
	}
	return fmt.Errorf("refusing to remove the last admin (%s): no other account holds the admin role", excluding)
}
