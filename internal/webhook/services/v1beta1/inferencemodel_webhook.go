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
	"slices"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
)

// SetupInferenceModelWebhookWithManager registers the webhook for InferenceModel.
func SetupInferenceModelWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &servicesv1beta1.InferenceModel{}).
		WithValidator(&InferenceModelCustomValidator{Reader: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-services-plume-labs-io-v1beta1-inferencemodel,mutating=false,failurePolicy=fail,sideEffects=None,groups=services.plume-labs.io,resources=inferencemodels,verbs=delete,versions=v1beta1,name=vinferencemodel-v1beta1.kb.io,admissionReviewVersions=v1

// InferenceModelCustomValidator refuses to delete a model something serves: the
// instance would keep running and report SizeRefused until someone puts the
// model back. Everything else is the CRD's schema, so creating a model needs no
// webhook, which is what lets the manager seed the catalog before it serves one.
type InferenceModelCustomValidator struct {
	Reader client.Reader
}

// ValidateCreate admits: the webhook is not called for it.
func (v *InferenceModelCustomValidator) ValidateCreate(
	context.Context, *servicesv1beta1.InferenceModel,
) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate admits: the webhook is not called for it.
func (v *InferenceModelCustomValidator) ValidateUpdate(
	context.Context, *servicesv1beta1.InferenceModel, *servicesv1beta1.InferenceModel,
) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete lists what would lose its model.
func (v *InferenceModelCustomValidator) ValidateDelete(
	ctx context.Context, model *servicesv1beta1.InferenceModel,
) (admission.Warnings, error) {
	var services servicesv1beta1.FrameServiceList
	if err := v.Reader.List(ctx, &services); err != nil {
		return nil, fmt.Errorf("listing the FrameServices that may serve %s: %w", model.Name, err)
	}
	var users []string
	for i := range services.Items {
		s := &services.Items[i]
		if s.Spec.Type == "inference" && string(s.Spec.Parameters["model"]) == model.Name {
			users = append(users, s.Namespace+"/"+s.Name)
		}
	}
	if len(users) > 0 {
		slices.Sort(users)
		return nil, fmt.Errorf("inference model %s is served by %s", model.Name, strings.Join(users, ", "))
	}
	return nil, nil
}
