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
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

// nolint:unused
var framejoblog = logf.Log.WithName("framejob-resource")

var knownPipelines = map[string]bool{
	"neura-training-dag":   true,
	"neura-inference-dag":  true,
	"speculative-decoding": true,
}

// SetupFrameJobWebhookWithManager registers the webhook for FrameJob in the manager.
func SetupFrameJobWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1alpha1.FrameJob{}).
		WithValidator(&FrameJobCustomValidator{}).
		WithDefaulter(&FrameJobCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-frame-plume-labs-io-v1alpha1-framejob,mutating=true,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framejobs,verbs=create;update,versions=v1alpha1,name=mframejob-v1alpha1.kb.io,admissionReviewVersions=v1

type FrameJobCustomDefaulter struct{}

func (d *FrameJobCustomDefaulter) Default(_ context.Context, obj *framev1alpha1.FrameJob) error {
	if obj.Spec.Priority == "" {
		obj.Spec.Priority = "medium"
	}
	if obj.Spec.ServiceClass == "" {
		obj.Spec.ServiceClass = "LOW"
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1alpha1-framejob,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framejobs,verbs=create;update,versions=v1alpha1,name=vframejob-v1alpha1.kb.io,admissionReviewVersions=v1

type FrameJobCustomValidator struct{}

func (v *FrameJobCustomValidator) ValidateCreate(_ context.Context, obj *framev1alpha1.FrameJob) (admission.Warnings, error) {
	return validateFrameJob(obj)
}

func (v *FrameJobCustomValidator) ValidateUpdate(_ context.Context, _, newObj *framev1alpha1.FrameJob) (admission.Warnings, error) {
	return validateFrameJob(newObj)
}

func (v *FrameJobCustomValidator) ValidateDelete(_ context.Context, _ *framev1alpha1.FrameJob) (admission.Warnings, error) {
	return nil, nil
}

func validateFrameJob(job *framev1alpha1.FrameJob) (admission.Warnings, error) {
	if !knownPipelines[job.Spec.Pipeline] {
		known := make([]string, 0, len(knownPipelines))
		for k := range knownPipelines {
			known = append(known, k)
		}
		return admission.Warnings{fmt.Sprintf("pipeline %q not in known list %v; ensure WorkflowTemplate exists", job.Spec.Pipeline, known)}, nil
	}
	if job.Spec.GPUCount > 0 && job.Spec.ServiceClass == "LOW" {
		return nil, fmt.Errorf("jobs requesting GPUs must use serviceClass HIGH or MEDIUM, got LOW")
	}
	return nil, nil
}
