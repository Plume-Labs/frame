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
var schedulingpolicylog = logf.Log.WithName("schedulingpolicy-resource")

// SetupSchedulingPolicyWebhookWithManager registers the webhook for SchedulingPolicy in the manager.
func SetupSchedulingPolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1alpha1.SchedulingPolicy{}).
		WithValidator(&SchedulingPolicyCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1alpha1-schedulingpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=schedulingpolicies,verbs=create;update,versions=v1alpha1,name=vschedulingpolicy-v1alpha1.kb.io,admissionReviewVersions=v1

type SchedulingPolicyCustomValidator struct{}

func (v *SchedulingPolicyCustomValidator) ValidateCreate(_ context.Context, obj *framev1alpha1.SchedulingPolicy) (admission.Warnings, error) {
	return validateSchedulingPolicy(obj)
}

func (v *SchedulingPolicyCustomValidator) ValidateUpdate(_ context.Context, _, newObj *framev1alpha1.SchedulingPolicy) (admission.Warnings, error) {
	return validateSchedulingPolicy(newObj)
}

func (v *SchedulingPolicyCustomValidator) ValidateDelete(_ context.Context, _ *framev1alpha1.SchedulingPolicy) (admission.Warnings, error) {
	return nil, nil
}

func validateSchedulingPolicy(sp *framev1alpha1.SchedulingPolicy) (admission.Warnings, error) {
	if sp.Spec.Preemption && sp.Spec.PriorityClass == "" {
		return nil, fmt.Errorf("spec.priorityClass is required when preemption is true")
	}
	return nil, nil
}
