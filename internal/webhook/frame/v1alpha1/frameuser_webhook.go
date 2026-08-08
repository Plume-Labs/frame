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
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameusers,verbs=get;list;watch

// SetupFrameUserWebhookWithManager registers the webhook for FrameUser.
func SetupFrameUserWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1alpha1.FrameUser{}).
		WithValidator(&FrameUserCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1alpha1-frameuser,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=frameusers,verbs=create;update;delete,versions=v1alpha1,name=vframeuser-v1alpha1.kb.io,admissionReviewVersions=v1

// FrameUserCustomValidator keeps at least one admin in existence.
//
// This lives at admission rather than inside authd because authd is not the
// only writer: admins create, delete and re-role accounts straight through the
// apiserver under their own identity, and kubectl bypasses authd entirely.
// Admission is the only chokepoint every write passes through.
type FrameUserCustomValidator struct {
	Client client.Client
}

func (v *FrameUserCustomValidator) ValidateCreate(_ context.Context, _ *framev1alpha1.FrameUser) (admission.Warnings, error) {
	return nil, nil
}

func (v *FrameUserCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *framev1alpha1.FrameUser) (admission.Warnings, error) {
	// Only a demotion can remove an admin; anything else leaves the count alone.
	if oldObj.Spec.Role != framev1alpha1.RoleAdmin || newObj.Spec.Role == framev1alpha1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, oldObj.Name)
}

func (v *FrameUserCustomValidator) ValidateDelete(ctx context.Context, obj *framev1alpha1.FrameUser) (admission.Warnings, error) {
	if obj.Spec.Role != framev1alpha1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, obj.Name)
}

// requireAnotherAdmin fails unless some admin other than `excluding` exists.
func (v *FrameUserCustomValidator) requireAnotherAdmin(ctx context.Context, excluding string) error {
	var users framev1alpha1.FrameUserList
	if err := v.Client.List(ctx, &users); err != nil {
		// Fail closed: an unreadable list is not evidence that another admin
		// exists, and guessing wrong here locks everyone out of the UI.
		return fmt.Errorf("cannot verify remaining admins: %w", err)
	}
	for _, u := range users.Items {
		if u.Name != excluding && u.Spec.Role == framev1alpha1.RoleAdmin {
			return nil
		}
	}
	return fmt.Errorf("refusing to remove the last admin (%s): no other account holds the admin role", excluding)
}
