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
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	v1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Conversion between v1alpha1 (spoke) and v1beta1 (hub).
//
// Two rules govern every function here.
//
//  1. ConvertFrom must reproduce a v1alpha1 object faithfully enough that a
//     v1beta1 -> v1alpha1 -> v1beta1 round trip is *exactly* lossless. For
//     every kind but one, that is achievable without any annotation escape
//     hatch, because v1beta1 has no field v1alpha1 lacks: status.observed-
//     Generation and FrameResourceQuota's status.used/status.namespaces were
//     all added to v1alpha1 before the freeze, deliberately, so this
//     direction would be empty. FrameUser is the one kind where the
//     difference runs both ways, and there it is a rename
//     (spec.passwordHash <-> status.passwordHash) rather than an addition,
//     so it is a bijection and still needs no hatch. FrameJob is the
//     exception: spec.type and spec.container were added at v1beta1 after
//     the freeze (the typed-job-submission design), so this direction is not
//     empty there, and the hatch this rule otherwise avoids is exactly what
//     keeps FrameJob's round trip lossless too — see the note on FrameJob's
//     ConvertTo/ConvertFrom below for why a naive drop was unacceptable here
//     specifically.
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
//
// spec.type and spec.container (added at v1beta1 for the typed-job-
// submission design, stage 1) have no v1alpha1 equivalent, which breaks rule
// 1 above for FrameJob specifically: for the first time, v1beta1 has a field
// v1alpha1 lacks.
//
// The first version of this comment assumed the CEL rule on FrameJobSpec
// ("exactly one of pipeline or container") would turn a naive drop into a
// rejection: converting a container-typed job down to v1alpha1 and back up
// unchanged leaves *neither* field set, which the rule forbids. Proven wrong
// by internal/controller/frame/conversion_envtest_test.go, "a full v1alpha1
// PUT round trip of a container-typed FrameJob": the write is **accepted**.
// The apiserver validates a write against the *request* version's schema —
// v1alpha1 has no container field, so there is nothing for the CEL rule to
// see — and stores whatever the conversion webhook returns **without
// re-validating it against the storage version's schema**. That asymmetry is
// exactly the one docs/upgrading.md already documents for a v1alpha1 status
// patch on SchedulingPolicy evaluating a CEL rule against an absent
// spec.preemption key; this is its FrameJob-shaped twin, hit through a full
// spec PUT rather than a status patch. Concretely, unmitigated: a v1alpha1
// client that reads a container-typed FrameJob and writes it back verbatim
// silently erases spec.container, and spec.type resets to whatever the
// schema defaults it to (verified: "background", regardless of what it was) —
// producing a *stored* v1beta1 object that violates its own CEL invariant,
// with no error surfaced anywhere.
//
// That is real, silent data loss on a still-served version, not a rejection
// an old client can expect and handle, so it is closed here rather than
// merely documented: framejobContainerAnnotation stashes spec.container and
// a non-empty spec.type on the way down (ConvertFrom) and restores them on
// the way up (ConvertTo), removing the annotation so it never reaches
// storage. This is the standard multi-version-CRD escape hatch, and it is a
// different situation from the one rule 2 below declines to use it for:
// spec.namespace and TalosSecretReference.namespace are v1alpha1 fields a
// client can still see and be misled by if their now-inert value were
// preserved untouched; spec.container and spec.type have no v1alpha1 field
// to be misled by at all; the annotation is visibly out-of-band extra data,
// not a normal-looking field quietly lying. It restores rule 1 in full for
// FrameJob: with the annotation round-tripping, a v1beta1 -> v1alpha1 ->
// v1beta1 trip is exactly lossless again, proven by
// TestHubRoundTripIsLossless with no exception needed for this kind.
//
// This does not close every gap. A client that both sets spec.pipeline at
// v1alpha1 *and* carries a leftover container-stash annotation (from an
// earlier GET of a different, container-typed object, copied onto a new
// manifest by hand) reconstructs a spec violating the same CEL invariant on
// write, for the same reason: no re-validation after conversion. That is a
// client actively contradicting itself rather than an unchanged round trip,
// and is out of scope for this fix.
//
// Stage 2 (the controller dispatch) adds status.containerJobName and
// status.containerJobKind at v1beta1, the container-substrate counterpart to
// status.argoWorkflowName — and hits the identical gap: two v1beta1-only
// fields with nowhere to go at v1alpha1. Rather than invent a second
// annotation for a status-shaped version of the exact same problem, they
// ride in the same framejobContainerAnnotation payload; TestHubRoundTripIsLossless
// caught the drop the same way it caught the stage 1 one, before any manual
// reasoning about it was written down here.
const framejobContainerAnnotation = "frame.plume-labs.io/framejob-container"

// framejobContainerAnnotationPayload started (stage 1) as the two spec
// fields v1alpha1 cannot represent. Stage 2 adds two more, for the same
// reason and through the same mechanism rather than a second annotation:
// status.containerJobName/status.containerJobKind are new v1beta1-only
// fields (the container-substrate counterpart to status.argoWorkflowName,
// which v1alpha1 already has), so a naive drop would reproduce the exact
// silent-loss scenario ConvertTo/ConvertFrom's doc above already fixed once
// for spec.container/spec.type. Everything else already has a real
// v1alpha1 field to round-trip through.
type framejobContainerAnnotationPayload struct {
	Type             string                    `json:"type,omitempty"`
	Container        *framejobContainerPayload `json:"container,omitempty"`
	ContainerJobName string                    `json:"containerJobName,omitempty"`
	ContainerJobKind string                    `json:"containerJobKind,omitempty"`
}

// framejobContainerPayload mirrors v1beta1.ContainerSpec, except Command,
// Args and Env are wrapped behind a pointer to their slice type.
// encoding/json's omitempty on a pointer field checks the *pointer's*
// nilness, not the pointee's emptiness, so an empty-but-present slice
// (`[]string{}`) and an absent one (nil) come out as two different things on
// the far side of the round trip — which json.Marshal(v1beta1.ContainerSpec)
// directly cannot do, because ContainerSpec's own `omitempty` tags (needed
// so a typed Go client's zero values stay off the real API's wire, same
// reason Pipeline has one) collapse both to absent. Resources is the one
// field left as a plain value: corev1.ResourceRequirements carries its own
// omitempty tags on Limits/Requests/Claims from k8s.io/api, outside this
// package's reach, so nil-vs-empty for *those* three does not survive this
// annotation. That is judged acceptable rather than worth a second shadow
// type: unlike spec.container's presence (which this whole mechanism exists
// to protect), "no resource limits set" and "resource limits set to an
// explicitly empty map" have never been two different things a FrameJob
// caller could mean.
type framejobContainerPayload struct {
	Image   string           `json:"image"`
	Command *[]string        `json:"command,omitempty"`
	Args    *[]string        `json:"args,omitempty"`
	Env     *[]corev1.EnvVar `json:"env,omitempty"`
	// EnvFrom needs the same pointer-to-slice treatment as Command/Args/Env
	// and for the identical reason: ContainerSpec.EnvFrom is a brand new
	// v1beta1-only field (GAP 1 of the typed-job-submission follow-up), and
	// this whole payload type exists precisely because embedding
	// v1beta1.ContainerSpec directly, or copying it field-by-field without
	// this wrapper, loses the nil-vs-empty distinction the same way Command
	// and friends already do. It is not covered "for free" by this struct
	// existing — each field has to be listed here explicitly, which is the
	// mistake this project has already shipped twice on this branch: adding
	// a field to ContainerSpec without extending this payload passes `go
	// build` and even most tests, and only TestHubRoundTripIsLossless
	// (api/frame/v1alpha1/conversion_test.go), which fuzzes the whole
	// v1beta1.FrameJob including this field, catches the silent drop.
	EnvFrom   *[]corev1.EnvFromSource     `json:"envFrom,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

func toFramejobContainerPayload(c *v1beta1.ContainerSpec) *framejobContainerPayload {
	if c == nil {
		return nil
	}
	p := &framejobContainerPayload{Image: c.Image, Resources: c.Resources}
	if c.Command != nil {
		p.Command = &c.Command
	}
	if c.Args != nil {
		p.Args = &c.Args
	}
	if c.Env != nil {
		p.Env = &c.Env
	}
	if c.EnvFrom != nil {
		p.EnvFrom = &c.EnvFrom
	}
	return p
}

func fromFramejobContainerPayload(p *framejobContainerPayload) *v1beta1.ContainerSpec {
	if p == nil {
		return nil
	}
	c := &v1beta1.ContainerSpec{Image: p.Image, Resources: p.Resources}
	if p.Command != nil {
		c.Command = *p.Command
	}
	if p.Args != nil {
		c.Args = *p.Args
	}
	if p.Env != nil {
		c.Env = *p.Env
	}
	if p.EnvFrom != nil {
		c.EnvFrom = *p.EnvFrom
	}
	return c
}

// stashFrameJobContainer preserves src's spec.container and spec.type into
// an annotation on dst, since v1alpha1's FrameJobSpec has no field for
// either. Only writes the annotation when there is something to preserve —
// no annotation appears on a pipeline-only job's v1alpha1 read, which is
// every FrameJob that existed before this field did.
//
// dst.ObjectMeta was just set to src.ObjectMeta by assignment, which is a
// shallow copy: dst.Annotations and src.Annotations are still the *same*
// map. Mutating it in place would corrupt src, the hub object the apiserver
// is still holding — hence the copy before the write.
func stashFrameJobContainer(dst *FrameJob, src *v1beta1.FrameJob) error {
	if src.Spec.Container == nil && src.Spec.Type == "" &&
		src.Status.ContainerJobName == "" && src.Status.ContainerJobKind == "" {
		return nil
	}
	payload := framejobContainerAnnotationPayload{
		Type:             string(src.Spec.Type),
		Container:        toFramejobContainerPayload(src.Spec.Container),
		ContainerJobName: src.Status.ContainerJobName,
		ContainerJobKind: src.Status.ContainerJobKind,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("stashing spec.container/spec.type for v1alpha1: %w", err)
	}
	annotations := make(map[string]string, len(dst.Annotations)+1)
	for k, v := range dst.Annotations {
		annotations[k] = v
	}
	annotations[framejobContainerAnnotation] = string(raw)
	dst.Annotations = annotations
	return nil
}

// restoreFrameJobContainer is stashFrameJobContainer's inverse: it reads the
// annotation off src (a v1alpha1 object, possibly with no annotation at all
// — every pipeline-only job) and fills dst's spec.container/spec.type from
// it, then strips the annotation from dst so it never reaches v1beta1
// storage. The same shallow-copy-aliasing note applies: dst.Annotations was
// just aliased to src.Annotations by the ObjectMeta assignment above, so the
// strip clones rather than deletes in place.
func restoreFrameJobContainer(dst *v1beta1.FrameJob, src *FrameJob) error {
	raw, ok := src.Annotations[framejobContainerAnnotation]
	if !ok {
		return nil
	}
	var payload framejobContainerAnnotationPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return fmt.Errorf("restoring spec.container/spec.type from v1alpha1: %w", err)
	}
	dst.Spec.Type = v1beta1.WorkloadType(payload.Type)
	dst.Spec.Container = fromFramejobContainerPayload(payload.Container)
	dst.Status.ContainerJobName = payload.ContainerJobName
	dst.Status.ContainerJobKind = payload.ContainerJobKind

	if len(dst.Annotations) == 0 {
		return nil
	}
	annotations := make(map[string]string, len(dst.Annotations))
	for k, v := range dst.Annotations {
		if k != framejobContainerAnnotation {
			annotations[k] = v
		}
	}
	if len(annotations) == 0 {
		annotations = nil
	}
	dst.Annotations = annotations
	return nil
}

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

	if err := restoreFrameJobContainer(dst, src); err != nil {
		return err
	}

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

	if err := stashFrameJobContainer(dst, src); err != nil {
		return err
	}

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
	dst.Spec.State = src.Spec.State

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
	dst.Spec.State = src.Spec.State
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
