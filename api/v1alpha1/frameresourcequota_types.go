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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// FrameResourceQuotaSpec defines the desired state of FrameResourceQuota
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

	// MaxJobs is the maximum number of concurrent jobs.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxJobs int32 `json:"maxJobs,omitempty"`
}

// FrameResourceQuotaStatus defines the observed state of FrameResourceQuota.
type FrameResourceQuotaStatus struct {
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

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

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
