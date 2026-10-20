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
	"net"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// nolint:unused
var framenodelog = logf.Log.WithName("framenode-resource")

// SetupFrameNodeWebhookWithManager registers the webhook for FrameNode in the manager.
func SetupFrameNodeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1beta1.FrameNode{}).
		WithValidator(&FrameNodeCustomValidator{}).
		WithDefaulter(&FrameNodeCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-frame-plume-labs-io-v1beta1-framenode,mutating=true,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framenodes,verbs=create;update,versions=v1beta1,name=mframenode-v1beta1.kb.io,admissionReviewVersions=v1

type FrameNodeCustomDefaulter struct{}

func (d *FrameNodeCustomDefaulter) Default(_ context.Context, obj *framev1beta1.FrameNode) error {
	if obj.Spec.Hostname == "" {
		obj.Spec.Hostname = obj.Name
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1beta1-framenode,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framenodes,verbs=create;update,versions=v1beta1,name=vframenode-v1beta1.kb.io,admissionReviewVersions=v1

type FrameNodeCustomValidator struct{}

func (v *FrameNodeCustomValidator) ValidateCreate(_ context.Context, obj *framev1beta1.FrameNode) (admission.Warnings, error) {
	return validateFrameNode(obj)
}

func (v *FrameNodeCustomValidator) ValidateUpdate(_ context.Context, _, newObj *framev1beta1.FrameNode) (admission.Warnings, error) {
	return validateFrameNode(newObj)
}

func (v *FrameNodeCustomValidator) ValidateDelete(_ context.Context, _ *framev1beta1.FrameNode) (admission.Warnings, error) {
	return nil, nil
}

func validateFrameNode(fn *framev1beta1.FrameNode) (admission.Warnings, error) {
	if net.ParseIP(fn.Spec.IP) == nil {
		return nil, fmt.Errorf("spec.ip %q is not a valid IP address", fn.Spec.IP)
	}

	// Whatever network detail is present must be well-formed. None of it is
	// required: FrameNode classifies a node and no longer provisions one (the
	// Talos path, and spec.disk that gated it, are gone), so the network block
	// is descriptive.
	for _, dns := range fn.Spec.Network.DNS {
		if net.ParseIP(dns) == nil {
			return nil, fmt.Errorf("spec.network.dns entry %q is not a valid IP address", dns)
		}
	}
	return nil, nil
}
