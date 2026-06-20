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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SchedulingPolicySpec defines the desired state of SchedulingPolicy
type SchedulingPolicySpec struct {
	// Scheduler selects the scheduler implementation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=volcano;yunikorn;default
	Scheduler string `json:"scheduler"`

	// QueueName is the Volcano/YuniKorn queue to submit jobs to.
	// +optional
	QueueName string `json:"queueName,omitempty"`

	// PriorityClass is the default Kubernetes PriorityClass for jobs under this policy.
	// +optional
	PriorityClass string `json:"priorityClass,omitempty"`

	// GangScheduling enables all-or-nothing gang scheduling for jobs.
	// +optional
	// +kubebuilder:default=false
	GangScheduling bool `json:"gangScheduling,omitempty"`

	// Preemption allows higher-priority jobs to preempt lower-priority ones.
	// +optional
	// +kubebuilder:default=false
	Preemption bool `json:"preemption,omitempty"`
}

// SchedulingPolicyStatus defines the observed state of SchedulingPolicy.
type SchedulingPolicyStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the SchedulingPolicy resource.
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

// SchedulingPolicy is the Schema for the schedulingpolicies API
type SchedulingPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of SchedulingPolicy
	// +required
	Spec SchedulingPolicySpec `json:"spec"`

	// status defines the observed state of SchedulingPolicy
	// +optional
	Status SchedulingPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SchedulingPolicyList contains a list of SchedulingPolicy
type SchedulingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SchedulingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SchedulingPolicy{}, &SchedulingPolicyList{})
}
