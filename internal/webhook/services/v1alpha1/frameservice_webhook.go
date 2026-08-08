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
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

// inferenceAPIKeySecretSuffix mirrors the unexported apiKeySecretSuffix
// constant in internal/services/provider/inference/inference.go, which names
// the Secret that provider owns for its generated API key
// (<FrameService name>-inference-key). Duplicated here, rather than
// exported and imported, because this webhook is deliberately
// provider-agnostic everywhere else — it dispatches through the registry by
// spec.type and never imports a concrete provider package. This one check is
// the sole exception: see the comment on apiKeySecretSuffix in inference.go
// for why the collision is reachable and why it produces a confusing
// BindingConflict message if left to happen at reconcile time instead of
// being refused here.
const inferenceAPIKeySecretSuffix = "-inference-key"

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

	if name := svc.Spec.Binding.SecretName; name != "" {
		// A Secret name must be a valid DNS-1123 subdomain — the same rule
		// Kubernetes applies to the object itself. Left unvalidated, a name
		// like "My_Secret" is admitted, and every write the controller then
		// attempts against it fails identically on every reconcile: the
		// instance error-loops with backoff while status keeps reporting the
		// last-known-good phase and secretRef forever, actively lying rather
		// than merely stalling. checkNamespaceExists in binding.go rejects
		// spec.binding.projectTo entries the analogous way, with
		// IsDNS1123Label; this is the same idea one level up, at admission.
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return fmt.Errorf("spec.binding.secretName %q is not a valid Secret name: %s",
				name, strings.Join(errs, "; "))
		}

		// This exact coordinate collides with the inference provider's own
		// API-key Secret (see inferenceAPIKeySecretSuffix). Left unchecked,
		// claimNewCoordinates in binding.go finds that Secret already sitting
		// there, unrecorded in status.binding.projected, and permanently
		// degrades the instance with BindingConflict — a message that claims
		// the Secret "was not written by" this FrameService, when it was,
		// just by a different Secret this same FrameService owns. Refusing
		// it here, with its own reason, replaces that confusing message with
		// one that names the actual cause.
		if svc.Spec.Type == "inference" && name == svc.Name+inferenceAPIKeySecretSuffix {
			return fmt.Errorf(
				"spec.binding.secretName %q collides with the inference provider's own API-key Secret "+
					"for this FrameService; choose a different name",
				name)
		}
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
