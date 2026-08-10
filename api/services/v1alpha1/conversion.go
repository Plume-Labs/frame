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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/conversion"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
)

// Conversion between the services group's v1alpha1 (spoke) and v1beta1 (hub).
// The rules are the ones set out at the top of api/frame/v1alpha1/conversion.go
// and are not restated here; what differs for FrameService is only this:
//
//   - it is a strict subset, and the sole v1alpha1-only field is status.phase,
//     so ConvertTo drops exactly one thing and ConvertFrom computes it;
//   - the phase is computed from Ready.Status and the deletion timestamp, not
//     from Ready.Reason. See phase.go for why the reason cannot be used;
//   - ServiceClass and ParameterValue are the shared frame-group types, so
//     they come through framev1beta1 rather than being redeclared here.

func (src *FrameService) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*servicesv1beta1.FrameService)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.FrameService, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Type = src.Spec.Type
	dst.Spec.Parameters = toParameterValues(src.Spec.Parameters)
	dst.Spec.ServiceClass = framev1beta1.ServiceClass(src.Spec.ServiceClass)
	dst.Spec.Binding = servicesv1beta1.BindingSpec{
		SecretName: src.Spec.Binding.SecretName,
		ProjectTo:  src.Spec.Binding.ProjectTo,
	}
	dst.Spec.DeletionPolicy = src.Spec.DeletionPolicy

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.Binding = servicesv1beta1.BindingStatus{
		SecretRef: src.Status.Binding.SecretRef,
		Endpoint:  src.Status.Binding.Endpoint,
		Projected: toBetaProjected(src.Status.Binding.Projected),
	}
	dst.Status.Sizing = servicesv1beta1.Sizing{
		GPU:       src.Status.Sizing.GPU,
		GPUMemory: src.Status.Sizing.GPUMemory,
		CPU:       src.Status.Sizing.CPU,
		Memory:    src.Status.Sizing.Memory,
	}
	dst.Status.Provisioned = toBetaProvisioned(src.Status.Provisioned)
	// src.Status.Phase is dropped: conditions are the storage (F2).

	return nil
}

func (dst *FrameService) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*servicesv1beta1.FrameService)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.FrameService, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Type = src.Spec.Type
	dst.Spec.Parameters = fromParameterValues(src.Spec.Parameters)
	dst.Spec.ServiceClass = string(src.Spec.ServiceClass)
	dst.Spec.Binding = BindingSpec{
		SecretName: src.Spec.Binding.SecretName,
		ProjectTo:  src.Spec.Binding.ProjectTo,
	}
	dst.Spec.DeletionPolicy = src.Spec.DeletionPolicy

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.Binding = BindingStatus{
		SecretRef: src.Status.Binding.SecretRef,
		Endpoint:  src.Status.Binding.Endpoint,
		Projected: fromBetaProjected(src.Status.Binding.Projected),
	}
	dst.Status.Sizing = Sizing{
		GPU:       src.Status.Sizing.GPU,
		GPUMemory: src.Status.Sizing.GPUMemory,
		CPU:       src.Status.Sizing.CPU,
		Memory:    src.Status.Sizing.Memory,
	}
	dst.Status.Provisioned = fromBetaProvisioned(src.Status.Provisioned)
	// The deletion timestamp is on the object, not in the conditions, which is
	// why this projection takes it as an argument rather than digging it out.
	dst.Status.Phase = FrameServicePhaseFromStatus(!src.DeletionTimestamp.IsZero(), src.Status.Conditions)

	return nil
}

func toBetaProjected(in []ProjectedSecretRef) []servicesv1beta1.ProjectedSecretRef {
	if in == nil {
		return nil
	}
	out := make([]servicesv1beta1.ProjectedSecretRef, len(in))
	for i, p := range in {
		out[i] = servicesv1beta1.ProjectedSecretRef{Namespace: p.Namespace, Name: p.Name}
	}
	return out
}

func fromBetaProjected(in []servicesv1beta1.ProjectedSecretRef) []ProjectedSecretRef {
	if in == nil {
		return nil
	}
	out := make([]ProjectedSecretRef, len(in))
	for i, p := range in {
		out[i] = ProjectedSecretRef{Namespace: p.Namespace, Name: p.Name}
	}
	return out
}

func toBetaProvisioned(in []ProvisionedRef) []servicesv1beta1.ProvisionedRef {
	if in == nil {
		return nil
	}
	out := make([]servicesv1beta1.ProvisionedRef, len(in))
	for i, p := range in {
		out[i] = servicesv1beta1.ProvisionedRef{
			APIVersion: p.APIVersion,
			Kind:       p.Kind,
			Name:       p.Name,
			Namespace:  p.Namespace,
		}
	}
	return out
}

func fromBetaProvisioned(in []servicesv1beta1.ProvisionedRef) []ProvisionedRef {
	if in == nil {
		return nil
	}
	out := make([]ProvisionedRef, len(in))
	for i, p := range in {
		out[i] = ProvisionedRef{
			APIVersion: p.APIVersion,
			Kind:       p.Kind,
			Name:       p.Name,
			Namespace:  p.Namespace,
		}
	}
	return out
}

// The parameter map is bounded through framev1beta1.ParameterValue, a named
// type, because controller-gen emits additionalProperties.maxLength only from
// a map's value *type*. On the wire both versions are map[string]string.
//
// This is the trap the FrameService owner named: a fixture whose parameters
// map is nil exercises neither loop, so it passes while the cast is wrong in
// both directions. The fuzzed round trip is what fills it.
func toParameterValues(in map[string]string) map[string]framev1beta1.ParameterValue {
	if in == nil {
		return nil
	}
	out := make(map[string]framev1beta1.ParameterValue, len(in))
	for k, v := range in {
		out[k] = framev1beta1.ParameterValue(v)
	}
	return out
}

func fromParameterValues(in map[string]framev1beta1.ParameterValue) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = string(v)
	}
	return out
}
