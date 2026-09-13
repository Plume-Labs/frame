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

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

// SetupFrameStorageWebhookWithManager registers the webhook for FrameStorage.
func SetupFrameStorageWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1beta1.FrameStorage{}).
		WithValidator(&FrameStorageCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1beta1-framestorage,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framestorages,verbs=create;update,versions=v1beta1,name=vframestorage-v1beta1.kb.io,admissionReviewVersions=v1

type FrameStorageCustomValidator struct {
	Client client.Client
}

func (v *FrameStorageCustomValidator) ValidateCreate(ctx context.Context, obj *framev1beta1.FrameStorage) (admission.Warnings, error) {
	return nil, v.validateAdoption(ctx, obj)
}

// ValidateUpdate re-runs the adoption check only when spec.storageClassName
// changes. Once a Frame-owned entry's class exists — which happens as soon as
// the controller creates it — the class is no longer "not found", so an
// unconditional re-check would fall into the final refusal on every
// subsequent update forever, telling the operator to set adoptExisting: true,
// which would be a lie about ownership rather than a fix.
//
// The decision was already made, correctly, at creation: if the name is
// unchanged, there is nothing new to decide. If the name changes, the new
// name is an adoption decision the create-time check never saw, so the full
// check runs against it.
//
// Gating on an owner reference instead was considered and rejected: whether
// the controller has created that reference depends on timing, so the same
// update would be accepted or refused depending on how far reconciliation had
// gotten. Comparing old against new has no such dependency.
func (v *FrameStorageCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *framev1beta1.FrameStorage) (admission.Warnings, error) {
	if oldObj.Spec.StorageClassName == newObj.Spec.StorageClassName {
		return nil, nil
	}
	return nil, v.validateAdoption(ctx, newObj)
}

func (v *FrameStorageCustomValidator) ValidateDelete(_ context.Context, _ *framev1beta1.FrameStorage) (admission.Warnings, error) {
	return nil, nil
}

// validateAdoption refuses an entry that names a StorageClass which already
// exists, unless it says so. A Frame-created entry owns its class and
// deletes it with itself; an entry that silently adopted ceph-rbd would
// delete the class sixteen production volumes provision from.
//
// A lookup error other than NotFound is a refusal, not a pass: "I could not
// check whether this class exists" and "this class does not exist" lead to
// opposite decisions, and only one of them is safe.
func (v *FrameStorageCustomValidator) validateAdoption(ctx context.Context, fs *framev1beta1.FrameStorage) error {
	if fs.Spec.AdoptExisting {
		return nil
	}

	var sc storagev1.StorageClass
	err := v.Client.Get(ctx, types.NamespacedName{Name: fs.Spec.StorageClassName}, &sc)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf(
			"cannot determine whether StorageClass %q already exists (%w); refusing rather than risk adopting it",
			fs.Spec.StorageClassName, err,
		)
	}

	return fmt.Errorf(
		"StorageClass %q already exists and was not created by Frame: set spec.adoptExisting to true to reference it. "+
			"An adopted entry owns nothing and deletes nothing; without the flag, Frame would take ownership of a class "+
			"that other volumes already provision from",
		fs.Spec.StorageClassName,
	)
}

// sharedForType derives status.shared from the type. It lives here, next to
// the validator, so nothing anywhere can set it from a spec.
func sharedForType(t string) bool {
	switch t {
	case "ceph-rbd", "ceph-bucket":
		return true
	default:
		return false
	}
}
