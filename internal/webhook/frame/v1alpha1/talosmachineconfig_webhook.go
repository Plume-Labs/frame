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
	"net"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

// nolint:unused
var talosmachineconfiglog = logf.Log.WithName("talosmachineconfig-resource")

// SetupTalosMachineConfigWebhookWithManager registers the webhook for TalosMachineConfig in the manager.
func SetupTalosMachineConfigWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1alpha1.TalosMachineConfig{}).
		WithValidator(&TalosMachineConfigCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1alpha1-talosmachineconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=talosmachineconfigs,verbs=create;update,versions=v1alpha1,name=vtalosmachineconfig-v1alpha1.kb.io,admissionReviewVersions=v1

type TalosMachineConfigCustomValidator struct{}

func (v *TalosMachineConfigCustomValidator) ValidateCreate(_ context.Context, obj *framev1alpha1.TalosMachineConfig) (admission.Warnings, error) {
	return validateTalosMachineConfig(obj)
}

func (v *TalosMachineConfigCustomValidator) ValidateUpdate(_ context.Context, _, newObj *framev1alpha1.TalosMachineConfig) (admission.Warnings, error) {
	return validateTalosMachineConfig(newObj)
}

func (v *TalosMachineConfigCustomValidator) ValidateDelete(_ context.Context, _ *framev1alpha1.TalosMachineConfig) (admission.Warnings, error) {
	return nil, nil
}

func validateTalosMachineConfig(tmc *framev1alpha1.TalosMachineConfig) (admission.Warnings, error) {
	if tmc.Spec.ConfigPatch == "" && tmc.Spec.ConfigPatchRef == nil {
		return nil, fmt.Errorf("exactly one of spec.configPatch or spec.configPatchRef must be set")
	}
	if tmc.Spec.ConfigPatch != "" && tmc.Spec.ConfigPatchRef != nil {
		return nil, fmt.Errorf("spec.configPatch and spec.configPatchRef are mutually exclusive")
	}
	if err := validateTalosEndpoint(tmc.Spec.TalosEndpoint); err != nil {
		return nil, err
	}
	return nil, nil
}

func validateTalosEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("spec.talosEndpoint %q must be in host:port format: %w", endpoint, err)
	}
	if net.ParseIP(host) == nil {
		// allow hostname
		if host == "" {
			return fmt.Errorf("spec.talosEndpoint host is empty")
		}
	}
	if port == "" {
		return fmt.Errorf("spec.talosEndpoint port is empty")
	}
	return nil
}
