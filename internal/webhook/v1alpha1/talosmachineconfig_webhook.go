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

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var talosmachineconfiglog = logf.Log.WithName("talosmachineconfig-resource")

// SetupTalosMachineConfigWebhookWithManager registers the webhook for TalosMachineConfig in the manager.
func SetupTalosMachineConfigWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1alpha1.TalosMachineConfig{}).
		WithValidator(&TalosMachineConfigCustomValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1alpha1-talosmachineconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=talosmachineconfigs,verbs=create;update,versions=v1alpha1,name=vtalosmachineconfig-v1alpha1.kb.io,admissionReviewVersions=v1

// TalosMachineConfigCustomValidator struct is responsible for validating the TalosMachineConfig resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type TalosMachineConfigCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type TalosMachineConfig.
func (v *TalosMachineConfigCustomValidator) ValidateCreate(_ context.Context, obj *framev1alpha1.TalosMachineConfig) (admission.Warnings, error) {
	talosmachineconfiglog.Info("Validation for TalosMachineConfig upon creation", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type TalosMachineConfig.
func (v *TalosMachineConfigCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *framev1alpha1.TalosMachineConfig) (admission.Warnings, error) {
	talosmachineconfiglog.Info("Validation for TalosMachineConfig upon update", "name", newObj.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type TalosMachineConfig.
func (v *TalosMachineConfigCustomValidator) ValidateDelete(_ context.Context, obj *framev1alpha1.TalosMachineConfig) (admission.Warnings, error) {
	talosmachineconfiglog.Info("Validation for TalosMachineConfig upon deletion", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
