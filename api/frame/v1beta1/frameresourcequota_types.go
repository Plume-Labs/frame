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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FrameResourceQuotaSpec defines the desired state of FrameResourceQuota.
//
// The spec-level rule below is inherited from v1alpha1 unchanged and is one
// of the four object-level CEL rules the freeze makes permanent. Object-level
// rules are the ones ratcheting cannot save you from — it is per-schema-node,
// so an over-strict rule here permanently freezes a stored object — which is
// why this one is carried over verbatim and no new one is added. Verified
// against the live cluster: all three stored quotas set maxCPU and maxMemory,
// so the rule holds on every one of them.
//
// +kubebuilder:validation:XValidation:rule="(has(self.maxGPUs) && self.maxGPUs > 0) || has(self.maxCPU) || has(self.maxMemory) || (has(self.maxJobs) && self.maxJobs > 0)",message="at least one of maxGPUs, maxCPU, maxMemory, or maxJobs must be set"
type FrameResourceQuotaSpec struct {
	// ServiceClass this quota applies to. Required: a quota that does not say
	// what it caps is meaningless, which is why this one member of the shared
	// enum is not optional and not defaulted. The enum lives on the
	// ServiceClass type, not here.
	// +kubebuilder:validation:Required
	ServiceClass ServiceClass `json:"serviceClass"`

	// MaxGPUs is the maximum number of GPUs allocatable across all jobs in
	// this service class. Capped for the same reason as FrameJob.gpuCount
	// (T5): three orders of magnitude above the one physical card this
	// cluster has, so it constrains nothing real and turns a typo into a
	// validation error. All three stored quotas hold 0.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1024
	MaxGPUs int32 `json:"maxGPUs,omitempty"`

	// MaxCPU is the maximum total CPU allocatable.
	// +optional
	MaxCPU *resource.Quantity `json:"maxCPU,omitempty"`

	// MaxMemory is the maximum total memory allocatable.
	// +optional
	MaxMemory *resource.Quantity `json:"maxMemory,omitempty"`

	// MaxJobs is the maximum number of FrameJob objects that may exist in a
	// namespace of this service class. Projected as the object-count quota
	// count/framejobs.frame.plume-labs.io, which the apiserver enforces on
	// creation. Completed FrameJobs keep counting until they are deleted.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxJobs int32 `json:"maxJobs,omitempty"`
}

// FrameResourceQuotaStatus defines the observed state of FrameResourceQuota.
//
// No status.phase (F2): this kind never had one. Every field here already
// exists on v1alpha1 — Part 0's Task 7 added observedGeneration, used and
// namespaces there first, deliberately, so that v1beta1 has no field
// v1alpha1 lacks and Task 18's conversion functions need no annotation
// escape hatch.
type FrameResourceQuotaStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Used is the sum of status.used across every corev1.ResourceQuota this
	// object projects into. The keys are the ones buildResourceList writes:
	// limits.cpu, limits.memory, requests.nvidia.com/gpu and
	// count/framejobs.frame.plume-labs.io. A key no namespace reported is
	// absent, not zero — this is a set, so omitempty is right here and
	// deliberately wrong on Namespaces below.
	// +optional
	Used corev1.ResourceList `json:"used,omitempty"`

	// Namespaces is how many namespaces this quota currently projects into.
	// It is what makes Used interpretable: a zero Used with zero namespaces
	// means "nothing selected this quota", which is a different problem from
	// "selected, and idle". Unlike Used, this is a count rather than a set,
	// so 0 is a real measurement and is serialized rather than omitted:
	// absent means "not yet reconciled", 0 means "reconciled, matched
	// nothing". All three stored quotas currently match zero namespaces, so
	// the asymmetry is load-bearing on every object that exists.
	// +optional
	Namespaces int32 `json:"namespaces"`

	// Conditions represent the current state of the FrameResourceQuota
	// resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// This is the conversion hub and the storage version. The marker arrived here
// in the same change that turned the conversion webhook on; see the note on
// the v1alpha1 FrameJob for why the two could not be separated.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ServiceClass",type=string,JSONPath=".spec.serviceClass"
// +kubebuilder:printcolumn:name="Namespaces",type=integer,JSONPath=".status.namespaces"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameResourceQuota is the Schema for the frameresourcequotas API
type FrameResourceQuota struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of FrameResourceQuota
	// +required
	Spec FrameResourceQuotaSpec `json:"spec"`

	// status defines the observed state of FrameResourceQuota
	// +optional
	Status FrameResourceQuotaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameResourceQuotaList contains a list of FrameResourceQuota
type FrameResourceQuotaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameResourceQuota `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameResourceQuota{}, &FrameResourceQuotaList{})
}
