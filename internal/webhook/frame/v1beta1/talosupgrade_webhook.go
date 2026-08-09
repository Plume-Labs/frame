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
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// nolint:unused
var talosupgradelog = logf.Log.WithName("talosupgrade-resource")

// SetupTalosUpgradeWebhookWithManager registers the webhook for TalosUpgrade in the manager.
func SetupTalosUpgradeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1beta1.TalosUpgrade{}).
		WithValidator(&TalosUpgradeCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1beta1-talosupgrade,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=talosupgrades,verbs=create;update,versions=v1beta1,name=vtalosupgrade-v1beta1.kb.io,admissionReviewVersions=v1

type TalosUpgradeCustomValidator struct{}

func (v *TalosUpgradeCustomValidator) ValidateCreate(_ context.Context, obj *framev1beta1.TalosUpgrade) (admission.Warnings, error) {
	return validateTalosUpgrade(obj)
}

func (v *TalosUpgradeCustomValidator) ValidateUpdate(_ context.Context, _, newObj *framev1beta1.TalosUpgrade) (admission.Warnings, error) {
	return validateTalosUpgrade(newObj)
}

func (v *TalosUpgradeCustomValidator) ValidateDelete(_ context.Context, _ *framev1beta1.TalosUpgrade) (admission.Warnings, error) {
	return nil, nil
}

func validateTalosUpgrade(tu *framev1beta1.TalosUpgrade) (admission.Warnings, error) {
	if err := validateTalosEndpoint(tu.Spec.TalosEndpoint); err != nil {
		return nil, err
	}
	// Image must contain a tag (colon present after the last slash)
	img := tu.Spec.Image
	lastSlash := strings.LastIndex(img, "/")
	ref := img
	if lastSlash >= 0 {
		ref = img[lastSlash+1:]
	}
	if !strings.Contains(ref, ":") {
		return nil, fmt.Errorf("spec.image %q must include a tag (e.g. installer:v1.8.0)", img)
	}
	return nil, nil
}
