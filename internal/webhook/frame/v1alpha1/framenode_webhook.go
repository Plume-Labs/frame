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
var framenodelog = logf.Log.WithName("framenode-resource")

// SetupFrameNodeWebhookWithManager registers the webhook for FrameNode in the manager.
func SetupFrameNodeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1alpha1.FrameNode{}).
		WithValidator(&FrameNodeCustomValidator{}).
		WithDefaulter(&FrameNodeCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-frame-plume-labs-io-v1alpha1-framenode,mutating=true,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framenodes,verbs=create;update,versions=v1alpha1,name=mframenode-v1alpha1.kb.io,admissionReviewVersions=v1

type FrameNodeCustomDefaulter struct{}

func (d *FrameNodeCustomDefaulter) Default(_ context.Context, obj *framev1alpha1.FrameNode) error {
	if obj.Spec.Hostname == "" {
		obj.Spec.Hostname = obj.Name
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1alpha1-framenode,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framenodes,verbs=create;update,versions=v1alpha1,name=vframenode-v1alpha1.kb.io,admissionReviewVersions=v1

type FrameNodeCustomValidator struct{}

func (v *FrameNodeCustomValidator) ValidateCreate(_ context.Context, obj *framev1alpha1.FrameNode) (admission.Warnings, error) {
	return validateFrameNode(obj)
}

func (v *FrameNodeCustomValidator) ValidateUpdate(_ context.Context, _, newObj *framev1alpha1.FrameNode) (admission.Warnings, error) {
	return validateFrameNode(newObj)
}

func (v *FrameNodeCustomValidator) ValidateDelete(_ context.Context, _ *framev1alpha1.FrameNode) (admission.Warnings, error) {
	return nil, nil
}

func validateFrameNode(fn *framev1alpha1.FrameNode) (admission.Warnings, error) {
	if net.ParseIP(fn.Spec.IP) == nil {
		return nil, fmt.Errorf("spec.ip %q is not a valid IP address", fn.Spec.IP)
	}

	// Whatever network detail is present must be well-formed, in either phase.
	for _, dns := range fn.Spec.Network.DNS {
		if net.ParseIP(dns) == nil {
			return nil, fmt.Errorf("spec.network.dns entry %q is not a valid IP address", dns)
		}
	}

	// A FrameNode is created with nothing but an IP so the controller can reach
	// the machine's Talos maintenance API and report back the disks and NICs it
	// found; the operator picks from those and patches the spec to provision.
	// spec.disk is the gate the controller itself uses to tell the two phases
	// apart, so the webhook uses the same one. Requiring a network before
	// discovery had rejected that first create outright, which made the whole
	// provisioning flow — and the wizard driving it — unreachable.
	if fn.Spec.Disk == "" {
		return nil, nil
	}

	if fn.Spec.Network.Address == "" {
		return nil, fmt.Errorf("spec.network.address is required once spec.disk is set")
	}
	if fn.Spec.Network.Gateway == "" {
		return nil, fmt.Errorf("spec.network.gateway is required once spec.disk is set")
	}
	if len(fn.Spec.Network.DNS) == 0 {
		return nil, fmt.Errorf("spec.network.dns must have at least one entry once spec.disk is set")
	}
	return nil, nil
}
