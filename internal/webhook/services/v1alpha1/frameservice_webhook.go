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
	"regexp"
	"slices"
	"strconv"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

// nolint:unused
// log is for logging in this package.
var frameservicelog = logf.Log.WithName("frameservice-resource")

// SetupFrameServiceWebhookWithManager registers the webhook for FrameService in the manager.
func SetupFrameServiceWebhookWithManager(mgr ctrl.Manager, registry *provider.Registry) error {
	return ctrl.NewWebhookManagedBy(mgr, &servicesv1alpha1.FrameService{}).
		WithValidator(&FrameServiceCustomValidator{Registry: registry}).
		Complete()
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-services-plume-labs-io-v1alpha1-frameservice,mutating=false,failurePolicy=fail,sideEffects=None,groups=services.plume-labs.io,resources=frameservices,verbs=create;update,versions=v1alpha1,name=vframeservice-v1alpha1.kb.io,admissionReviewVersions=v1

// FrameServiceCustomValidator enforces what the CRD cannot.
//
// spec.parameters is a free-form map by design, so the apiserver's own schema
// cannot check it. Rather than let every mistake surface ten seconds later as a
// degraded status, this validator dispatches on spec.type to the schema the
// provider registers, and runs the provider's Size so an instance that will
// never fit is refused by kubectl rather than admitted and left Pending.
type FrameServiceCustomValidator struct {
	Registry *provider.Registry
}

func (v *FrameServiceCustomValidator) ValidateCreate(
	_ context.Context, svc *servicesv1alpha1.FrameService,
) (admission.Warnings, error) {
	return nil, v.validate(svc)
}

func (v *FrameServiceCustomValidator) ValidateUpdate(
	_ context.Context, oldObj, newObj *servicesv1alpha1.FrameService,
) (admission.Warnings, error) {
	// The provisioned workload belongs to the provider that made it. Switching
	// type would orphan it: the new provider does not recognise it, and the old
	// one is no longer consulted.
	if oldObj.Spec.Type != "" && oldObj.Spec.Type != newObj.Spec.Type {
		return nil, fmt.Errorf(
			"spec.type is immutable: it is %q and cannot become %q. Delete the service and create a new one",
			oldObj.Spec.Type, newObj.Spec.Type)
	}
	return nil, v.validate(newObj)
}

func (v *FrameServiceCustomValidator) ValidateDelete(
	_ context.Context, _ *servicesv1alpha1.FrameService,
) (admission.Warnings, error) {
	return nil, nil
}

func (v *FrameServiceCustomValidator) validate(svc *servicesv1alpha1.FrameService) error {
	p, err := v.Registry.Get(svc.Spec.Type)
	if err != nil {
		return fmt.Errorf("spec.type: %w", err)
	}

	if err := validateAgainstSchema(p.ParameterSchema(), svc.Spec.Parameters); err != nil {
		return fmt.Errorf("spec.parameters: %w", err)
	}

	if _, err := p.Size(svc.Spec.Parameters); err != nil {
		return fmt.Errorf("spec.parameters: %w", err)
	}

	if slices.Contains(svc.Spec.Binding.ProjectTo, "") {
		return fmt.Errorf("spec.binding.projectTo contains an empty namespace")
	}
	return nil
}

// validateAgainstSchema checks a parameter map against a provider's schema.
//
// Parameters are map[string]string, so the schema's job here is presence,
// allowed values and shape — not types. Running the full JSON Schema validator
// would mean converting the map to unstructured JSON on every admission for
// checks this covers directly.
func validateAgainstSchema(schema *provider.Schema, params map[string]string) error {
	for _, required := range schema.Required {
		if params[required] == "" {
			return fmt.Errorf("%s is required", required)
		}
	}

	for key, value := range params {
		prop, known := schema.Properties[key]
		if !known {
			return fmt.Errorf("%s is not a parameter this type accepts", key)
		}
		if len(prop.Enum) > 0 && !enumAllows(prop.Enum, value) {
			return fmt.Errorf("%s: %q is not one of the accepted values", key, value)
		}
		if prop.Pattern != "" {
			ok, err := regexp.MatchString(prop.Pattern, value)
			if err != nil || !ok {
				return fmt.Errorf("%s: %q does not match %s", key, value, prop.Pattern)
			}
		}
	}
	return nil
}

func enumAllows(enum []apiextensionsv1.JSON, value string) bool {
	quoted := strconv.Quote(value)
	for _, allowed := range enum {
		if string(allowed.Raw) == quoted {
			return true
		}
	}
	return false
}
