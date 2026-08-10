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

	v1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Conversion between v1alpha1 (spoke) and v1beta1 (hub).
//
// Two rules govern every function here.
//
//  1. ConvertFrom must reproduce a v1alpha1 object faithfully enough that a
//     v1beta1 -> v1alpha1 -> v1beta1 round trip is *exactly* lossless. That is
//     achievable without any annotation escape hatch because v1beta1 has no
//     field v1alpha1 lacks: status.observedGeneration and
//     FrameResourceQuota's status.used/status.namespaces were all added to
//     v1alpha1 before the freeze, deliberately, so this direction would be
//     empty. FrameUser is the one kind where the difference runs both ways,
//     and there it is a rename (spec.passwordHash <-> status.passwordHash)
//     rather than an addition, so it is a bijection and still needs no hatch.
//
//  2. ConvertTo may normalise, and does so in exactly two places —
//     FrameJob.spec.namespace and TalosSecretReference.namespace. Both name a
//     namespace the CR does not live in, both are removed in v1beta1, and
//     both are announced in the version's deprecation warning. They are not
//     stashed in an annotation: a v1alpha1 client reading back
//     `namespace: other-ns` and believing it still works would be worse than
//     seeing the value the operator actually acts on.
//
// status.phase is never carried in either direction. It is computed on the way
// down, out of conditions, and never stored (F2). See phase.go.
//
// TypeMeta is deliberately not copied in either direction: controller-runtime's
// conversion handler stamps the destination's apiVersion and kind itself, and
// carrying the source's would name the wrong version.
//
// "Exactly lossless" includes nil-versus-empty. Every slice rebuilt here is
// allocated only when its source is non-nil, so an empty-but-present list
// stays empty-but-present and an absent one stays absent. The two are
// indistinguishable on the wire under omitempty, but the fuzzed round trip
// compares Go values, and quietly normalising one into the other would hide a
// real conversion mistake behind a cmp option.

// --- FrameJob ---------------------------------------------------------------

func (src *FrameJob) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.FrameJob)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.FrameJob, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Pipeline = src.Spec.Pipeline
	dst.Spec.ServiceClass = v1beta1.ServiceClass(src.Spec.ServiceClass)
	dst.Spec.Priority = src.Spec.Priority
	dst.Spec.GPUCount = src.Spec.GPUCount
	dst.Spec.Suspended = src.Spec.Suspended
	dst.Spec.Parameters = toParameterValues(src.Spec.Parameters)
	// src.Spec.Namespace is dropped: the Workflow is created beside its
	// FrameJob now (F5). Every stored FrameJob set it to its own namespace,
	// so this is a no-op on everything that exists.

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.ArgoWorkflowName = src.Status.ArgoWorkflowName
	dst.Status.StartTime = src.Status.StartTime
	dst.Status.CompletionTime = src.Status.CompletionTime
	dst.Status.Message = src.Status.Message
	// src.Status.Phase is dropped: conditions are the storage (F2).

	return nil
}

func (dst *FrameJob) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.FrameJob)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.FrameJob, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Pipeline = src.Spec.Pipeline
	dst.Spec.ServiceClass = string(src.Spec.ServiceClass)
	dst.Spec.Priority = src.Spec.Priority
	dst.Spec.GPUCount = src.Spec.GPUCount
	dst.Spec.Suspended = src.Spec.Suspended
	dst.Spec.Parameters = fromParameterValues(src.Spec.Parameters)
	// The one honest answer for a field that no longer exists: the namespace
	// the operator actually acts in, which is the object's own.
	dst.Spec.Namespace = src.Namespace

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.ArgoWorkflowName = src.Status.ArgoWorkflowName
	dst.Status.StartTime = src.Status.StartTime
	dst.Status.CompletionTime = src.Status.CompletionTime
	dst.Status.Message = src.Status.Message
	dst.Status.Phase = FrameJobPhaseFromConditions(src.Status.Conditions)

	return nil
}

// --- FrameNode --------------------------------------------------------------

func (src *FrameNode) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.FrameNode)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.FrameNode, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.IP = src.Spec.IP
	dst.Spec.Role = src.Spec.Role
	dst.Spec.Disk = src.Spec.Disk
	dst.Spec.RDMAInterface = src.Spec.RDMAInterface
	dst.Spec.Hostname = src.Spec.Hostname
	dst.Spec.Rack = src.Spec.Rack
	dst.Spec.Zone = src.Spec.Zone
	dst.Spec.ServiceClass = v1beta1.ServiceClass(src.Spec.ServiceClass)
	// network.address holds a CIDR on all three stored nodes and carries no
	// isIP rule on either version, so it copies verbatim like every other
	// member. Adding a normalisation here would strand them.
	dst.Spec.Network = v1beta1.NetworkSpec{
		Address: src.Spec.Network.Address,
		Gateway: src.Spec.Network.Gateway,
		DNS:     src.Spec.Network.DNS,
		VLAN:    src.Spec.Network.VLAN,
		Bond:    src.Spec.Network.Bond,
	}

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.DiscoveredHostname = src.Status.DiscoveredHostname
	dst.Status.DiscoveredTalosVersion = src.Status.DiscoveredTalosVersion
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.KubeletVersion = src.Status.KubeletVersion
	dst.Status.Capacity = src.Status.Capacity
	dst.Status.Allocatable = src.Status.Allocatable
	dst.Status.NodeName = src.Status.NodeName
	if src.Status.DiscoveredDisks != nil {
		dst.Status.DiscoveredDisks = make([]v1beta1.DiskInfo, len(src.Status.DiscoveredDisks))
		for i, d := range src.Status.DiscoveredDisks {
			dst.Status.DiscoveredDisks[i] = v1beta1.DiskInfo{Name: d.Name, Size: d.Size, Type: d.Type}
		}
	}
	if src.Status.DiscoveredNICs != nil {
		dst.Status.DiscoveredNICs = make([]v1beta1.NICInfo, len(src.Status.DiscoveredNICs))
		for i, n := range src.Status.DiscoveredNICs {
			dst.Status.DiscoveredNICs[i] = v1beta1.NICInfo{Name: n.Name, MAC: n.MAC, Speed: n.Speed}
		}
	}
	// src.Status.Phase is dropped: conditions are the storage (F2).

	return nil
}

func (dst *FrameNode) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.FrameNode)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.FrameNode, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.IP = src.Spec.IP
	dst.Spec.Role = src.Spec.Role
	dst.Spec.Disk = src.Spec.Disk
	dst.Spec.RDMAInterface = src.Spec.RDMAInterface
	dst.Spec.Hostname = src.Spec.Hostname
	dst.Spec.Rack = src.Spec.Rack
	dst.Spec.Zone = src.Spec.Zone
	dst.Spec.ServiceClass = string(src.Spec.ServiceClass)
	dst.Spec.Network = NetworkSpec{
		Address: src.Spec.Network.Address,
		Gateway: src.Spec.Network.Gateway,
		DNS:     src.Spec.Network.DNS,
		VLAN:    src.Spec.Network.VLAN,
		Bond:    src.Spec.Network.Bond,
	}

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.DiscoveredHostname = src.Status.DiscoveredHostname
	dst.Status.DiscoveredTalosVersion = src.Status.DiscoveredTalosVersion
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.KubeletVersion = src.Status.KubeletVersion
	dst.Status.Capacity = src.Status.Capacity
	dst.Status.Allocatable = src.Status.Allocatable
	dst.Status.NodeName = src.Status.NodeName
	if src.Status.DiscoveredDisks != nil {
		dst.Status.DiscoveredDisks = make([]DiskInfo, len(src.Status.DiscoveredDisks))
		for i, d := range src.Status.DiscoveredDisks {
			dst.Status.DiscoveredDisks[i] = DiskInfo{Name: d.Name, Size: d.Size, Type: d.Type}
		}
	}
	if src.Status.DiscoveredNICs != nil {
		dst.Status.DiscoveredNICs = make([]NICInfo, len(src.Status.DiscoveredNICs))
		for i, n := range src.Status.DiscoveredNICs {
			dst.Status.DiscoveredNICs[i] = NICInfo{Name: n.Name, MAC: n.MAC, Speed: n.Speed}
		}
	}
	dst.Status.Phase = FrameNodePhaseFromConditions(src.Status.Conditions)

	return nil
}

// --- FrameResourceQuota -----------------------------------------------------

func (src *FrameResourceQuota) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.FrameResourceQuota)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.FrameResourceQuota, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.ServiceClass = v1beta1.ServiceClass(src.Spec.ServiceClass)
	dst.Spec.MaxGPUs = src.Spec.MaxGPUs
	dst.Spec.MaxCPU = src.Spec.MaxCPU
	dst.Spec.MaxMemory = src.Spec.MaxMemory
	dst.Spec.MaxJobs = src.Spec.MaxJobs

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Used = src.Status.Used
	dst.Status.Namespaces = src.Status.Namespaces
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

func (dst *FrameResourceQuota) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.FrameResourceQuota)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.FrameResourceQuota, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.ServiceClass = string(src.Spec.ServiceClass)
	dst.Spec.MaxGPUs = src.Spec.MaxGPUs
	dst.Spec.MaxCPU = src.Spec.MaxCPU
	dst.Spec.MaxMemory = src.Spec.MaxMemory
	dst.Spec.MaxJobs = src.Spec.MaxJobs

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Used = src.Status.Used
	dst.Status.Namespaces = src.Status.Namespaces
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

// --- SchedulingPolicy -------------------------------------------------------

func (src *SchedulingPolicy) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.SchedulingPolicy)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.SchedulingPolicy, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Scheduler = src.Spec.Scheduler
	dst.Spec.QueueName = src.Spec.QueueName
	dst.Spec.PriorityClass = src.Spec.PriorityClass
	dst.Spec.Preemption = src.Spec.Preemption
	dst.Spec.PriorityValue = src.Spec.PriorityValue
	dst.Spec.QueueWeight = src.Spec.QueueWeight

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

func (dst *SchedulingPolicy) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.SchedulingPolicy)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.SchedulingPolicy, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Scheduler = src.Spec.Scheduler
	dst.Spec.QueueName = src.Spec.QueueName
	dst.Spec.PriorityClass = src.Spec.PriorityClass
	dst.Spec.Preemption = src.Spec.Preemption
	dst.Spec.PriorityValue = src.Spec.PriorityValue
	dst.Spec.QueueWeight = src.Spec.QueueWeight

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

// --- TalosMachineConfig -----------------------------------------------------

func (src *TalosMachineConfig) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.TalosMachineConfig)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.TalosMachineConfig, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.NodeName = src.Spec.NodeName
	dst.Spec.TalosEndpoint = src.Spec.TalosEndpoint
	// Namespace is dropped (F6). See the note on ConvertFrom for why it is not
	// stashed anywhere.
	dst.Spec.TalosSecretRef = v1beta1.TalosSecretReference{Name: src.Spec.TalosSecretRef.Name}
	dst.Spec.ConfigPatch = src.Spec.ConfigPatch
	dst.Spec.ConfigPatchRef = src.Spec.ConfigPatchRef

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

func (dst *TalosMachineConfig) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.TalosMachineConfig)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.TalosMachineConfig, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.NodeName = src.Spec.NodeName
	dst.Spec.TalosEndpoint = src.Spec.TalosEndpoint
	// Namespace comes back empty rather than stashed: empty already meant
	// "this CR's own namespace" — buildTalosClient falls back to it
	// explicitly — so the normalised value is *the truth*, not a placeholder.
	dst.Spec.TalosSecretRef = TalosSecretReference{Name: src.Spec.TalosSecretRef.Name}
	dst.Spec.ConfigPatch = src.Spec.ConfigPatch
	dst.Spec.ConfigPatchRef = src.Spec.ConfigPatchRef

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

// --- TalosUpgrade -----------------------------------------------------------

func (src *TalosUpgrade) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.TalosUpgrade)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.TalosUpgrade, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.NodeName = src.Spec.NodeName
	dst.Spec.TalosEndpoint = src.Spec.TalosEndpoint
	dst.Spec.TalosSecretRef = v1beta1.TalosSecretReference{Name: src.Spec.TalosSecretRef.Name}
	dst.Spec.Image = src.Spec.Image

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

func (dst *TalosUpgrade) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.TalosUpgrade)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.TalosUpgrade, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.NodeName = src.Spec.NodeName
	dst.Spec.TalosEndpoint = src.Spec.TalosEndpoint
	dst.Spec.TalosSecretRef = TalosSecretReference{Name: src.Spec.TalosSecretRef.Name}
	dst.Spec.Image = src.Spec.Image

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions

	return nil
}

// --- FrameUser --------------------------------------------------------------

// FrameUser is the one kind here that is a bijection rather than a subset. The
// password hash moves section — v1alpha1 spec.passwordHash is v1beta1
// status.passwordHash (F11) — so both directions carry it, and dropping it in
// either one silently breaks password login with a 200 response.

func (src *FrameUser) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.FrameUser)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.FrameUser, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Email = src.Spec.Email
	dst.Spec.Role = src.Spec.Role
	dst.Spec.PasswordAuth = src.Spec.PasswordAuth

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.PasswordHash = src.Spec.PasswordHash
	dst.Status.Credentials = toBetaCredentials(src.Status.Credentials)

	return nil
}

func (dst *FrameUser) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.FrameUser)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.FrameUser, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Email = src.Spec.Email
	dst.Spec.Role = src.Spec.Role
	dst.Spec.PasswordAuth = src.Spec.PasswordAuth
	dst.Spec.PasswordHash = src.Status.PasswordHash

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Credentials = fromBetaCredentials(src.Status.Credentials)

	return nil
}

func toBetaCredentials(in []WebAuthnCredential) []v1beta1.WebAuthnCredential {
	if in == nil {
		return nil
	}
	out := make([]v1beta1.WebAuthnCredential, len(in))
	for i, c := range in {
		out[i] = v1beta1.WebAuthnCredential{
			ID:        c.ID,
			PublicKey: c.PublicKey,
			SignCount: c.SignCount,
			AddedAt:   c.AddedAt,
			Label:     c.Label,
		}
	}
	return out
}

func fromBetaCredentials(in []v1beta1.WebAuthnCredential) []WebAuthnCredential {
	if in == nil {
		return nil
	}
	out := make([]WebAuthnCredential, len(in))
	for i, c := range in {
		out[i] = WebAuthnCredential{
			ID:        c.ID,
			PublicKey: c.PublicKey,
			SignCount: c.SignCount,
			AddedAt:   c.AddedAt,
			Label:     c.Label,
		}
	}
	return out
}

// --- parameter maps ---------------------------------------------------------

// v1beta1 bounds parameter values through a named type, which is the only
// way controller-gen emits additionalProperties.maxLength. On the wire both
// versions are map[string]string; these two functions are the Go-side cost.
func toParameterValues(in map[string]string) map[string]v1beta1.ParameterValue {
	if in == nil {
		return nil
	}
	out := make(map[string]v1beta1.ParameterValue, len(in))
	for k, v := range in {
		out[k] = v1beta1.ParameterValue(v)
	}
	return out
}

func fromParameterValues(in map[string]v1beta1.ParameterValue) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = string(v)
	}
	return out
}
