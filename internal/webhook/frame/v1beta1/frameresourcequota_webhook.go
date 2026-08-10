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
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// nolint:unused
var frameresourcequotalog = logf.Log.WithName("frameresourcequota-resource")

// SetupFrameResourceQuotaWebhookWithManager registers the webhook for FrameResourceQuota in the manager.
func SetupFrameResourceQuotaWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1beta1.FrameResourceQuota{}).
		WithValidator(&FrameResourceQuotaCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1beta1-frameresourcequota,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=frameresourcequotas,verbs=create;update,versions=v1beta1,name=vframeresourcequota-v1beta1.kb.io,admissionReviewVersions=v1

type FrameResourceQuotaCustomValidator struct{}

func (v *FrameResourceQuotaCustomValidator) ValidateCreate(_ context.Context, obj *framev1beta1.FrameResourceQuota) (admission.Warnings, error) {
	return validateFrameResourceQuota(obj)
}

func (v *FrameResourceQuotaCustomValidator) ValidateUpdate(_ context.Context, _, newObj *framev1beta1.FrameResourceQuota) (admission.Warnings, error) {
	return validateFrameResourceQuota(newObj)
}

func (v *FrameResourceQuotaCustomValidator) ValidateDelete(_ context.Context, _ *framev1beta1.FrameResourceQuota) (admission.Warnings, error) {
	return nil, nil
}

func validateFrameResourceQuota(frq *framev1beta1.FrameResourceQuota) (admission.Warnings, error) {
	if frq.Spec.MaxGPUs == 0 && frq.Spec.MaxCPU == nil && frq.Spec.MaxMemory == nil && frq.Spec.MaxJobs == 0 {
		return nil, fmt.Errorf("at least one limit (maxGPUs, maxCPU, maxMemory, maxJobs) must be set")
	}
	return nil, nil
}
