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

// FrameUserCustomValidator keeps at least one admin in existence, and keeps
// status.passwordHash off every write path except the status subresource.
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
	// Only a demotion can remove an admin; anything else leaves the count alone.
	if oldObj.Spec.Role != framev1beta1.RoleAdmin || newObj.Spec.Role == framev1beta1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, oldObj.Name)
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
