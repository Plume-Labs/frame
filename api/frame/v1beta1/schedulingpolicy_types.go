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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SchedulingPolicySpec defines the desired state of SchedulingPolicy.
//
// The spec-level rule below is inherited from v1alpha1 unchanged, one of the
// four object-level CEL rules the freeze makes permanent. It carries over
// verbatim and nothing is added beside it: ratcheting is per-schema-node, so
// an over-strict object-level rule permanently freezes a stored object.
// Verified against the live cluster: the one stored policy sets
// preemption: true with priorityClass: neura-high, which satisfies it.
//
// +kubebuilder:validation:XValidation:rule="!self.preemption || (has(self.priorityClass) && size(self.priorityClass) > 0)",message="priorityClass is required when preemption is true"
type SchedulingPolicySpec struct {
	// Scheduler selects the scheduler implementation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=volcano;yunikorn;default
	Scheduler string `json:"scheduler"`

	// QueueName is the Volcano/YuniKorn queue to submit jobs to. Not an enum:
	// it names an externally-created Queue object, but it is still a
	// Kubernetes object name, hence the pattern. The pattern accepts empty:
	// the controller branches on "" to skip queue reconciliation, and the
	// SDK's create form sends this field unconditionally, so a user clearing
	// it must still be able to save.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?$"
	QueueName string `json:"queueName,omitempty"`

	// PriorityClass is the default Kubernetes PriorityClass for jobs under
	// this policy. Accepts empty for the same reason as QueueName.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?$"
	PriorityClass string `json:"priorityClass,omitempty"`

	// Preemption allows higher-priority jobs to preempt lower-priority ones.
	// Setting it obliges PriorityClass, per the object-level rule above.
	// +optional
	// +kubebuilder:default=false
	Preemption bool `json:"preemption,omitempty"`

	// PriorityValue is the integer value for the Kubernetes PriorityClass.
	// Ignored when PriorityClass is empty. Higher values = higher priority;
	// system pods use 2000000000.
	// +optional
	// +kubebuilder:validation:Minimum=-2147483648
	// +kubebuilder:validation:Maximum=1000000000
	PriorityValue *int32 `json:"priorityValue,omitempty"`

	// QueueWeight is the relative weight of the Volcano/YuniKorn queue
	// (default 1).
	//
	// The ceiling is new (T5). PriorityValue beside it was already bounded,
	// so the absence here was inconsistency rather than policy, and a bound
	// can only ever be introduced before the freeze — adding one afterwards
	// rejects objects that were valid the day before. The one stored policy
	// holds 100. Field-level, so ratcheting protects anything stored.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000000
	QueueWeight *int32 `json:"queueWeight,omitempty"`
}

// SchedulingPolicyStatus defines the observed state of SchedulingPolicy.
//
// No status.phase (F2): this kind never had one, it was already
// conditions-only, and every field here already exists on v1alpha1.
type SchedulingPolicyStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the SchedulingPolicy
	// resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// This is the conversion hub, but it is deliberately *not* the storage
// version yet — v1alpha1 still carries +kubebuilder:storageversion, and
// Task 19 moves the marker here when the conversion webhook starts serving.
// See the note on the v1alpha1 FrameJob for the full reasoning.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Scheduler",type=string,JSONPath=".spec.scheduler"
// +kubebuilder:printcolumn:name="Queue",type=string,JSONPath=".spec.queueName"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

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
