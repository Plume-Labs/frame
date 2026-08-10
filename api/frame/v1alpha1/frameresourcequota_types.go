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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// FrameResourceQuotaSpec defines the desired state of FrameResourceQuota
//
// +kubebuilder:validation:XValidation:rule="(has(self.maxGPUs) && self.maxGPUs > 0) || has(self.maxCPU) || has(self.maxMemory) || (has(self.maxJobs) && self.maxJobs > 0)",message="at least one of maxGPUs, maxCPU, maxMemory, or maxJobs must be set"
type FrameResourceQuotaSpec struct {
	// ServiceClass this quota applies to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW
	ServiceClass string `json:"serviceClass"`

	// MaxGPUs is the maximum number of GPUs allocatable across all jobs in this service class.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxGPUs int32 `json:"maxGPUs,omitempty"`

	// MaxCPU is the maximum total CPU allocatable.
	// +optional
	MaxCPU *resource.Quantity `json:"maxCPU,omitempty"`

	// MaxMemory is the maximum total memory allocatable.
	// +optional
	MaxMemory *resource.Quantity `json:"maxMemory,omitempty"`

	// MaxJobs is the maximum number of FrameJob objects that may exist in a
	// namespace of this service class. It is projected as the object-count quota
	// count/framejobs.frame.plume-labs.io, which the apiserver enforces on
	// creation. Completed FrameJobs keep counting until they are deleted.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxJobs int32 `json:"maxJobs,omitempty"`
}

// FrameResourceQuotaStatus defines the observed state of FrameResourceQuota.
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
	// count/framejobs.frame.plume-labs.io. Absent until at least one
	// projected quota reports usage.
	// +optional
	Used corev1.ResourceList `json:"used,omitempty"`

	// Namespaces is how many namespaces this quota currently projects into.
	// It is what makes Used interpretable: a zero Used with zero namespaces
	// means "nothing selected this quota", which is a different problem from
	// "selected, and idle". Unlike Used, this is a count rather than a set,
	// so 0 is a real measurement and is serialized rather than omitted:
	// absent means "not yet reconciled", 0 means "reconciled, matched
	// nothing".
	// +optional
	Namespaces int32 `json:"namespaces"`

	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the FrameResourceQuota resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// This version is served and deprecated; v1beta1 is the storage version. The
// marker moved there in the same change that turned the conversion webhook on
// — see the note on the v1alpha1 FrameJob for why those two could not be
// separated. FrameResourceQuota is the easy case: it has no field the other
// version lacks in either direction (Part 0's Task 7 put observedGeneration,
// used and namespaces here first), so neither placement ever pruned anything.
//
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 FrameResourceQuota is deprecated; use frame.plume-labs.io/v1beta1."
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ServiceClass",type=string,JSONPath=".spec.serviceClass"
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
