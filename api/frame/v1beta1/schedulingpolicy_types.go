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
// The has() guard is not decoration. preemption is `bool json:",omitempty"`
// with `default: false`, so an object that never set it has no preemption key
// on the wire. The apiserver defaults it on a full write at the request
// version — but the conversion webhook's output is not re-defaulted, so once
// v1beta1 became the storage version, `kubectl patch`/`client.Status().Patch`
// through a v1alpha1 client evaluated this rule against a key-less object and
// was rejected with `no such key: preemption`. That is every reconcile of the
// SchedulingPolicy controller. Reproduced in envtest before the guard was
// added. The guard only widens what the rule admits: an absent key means
// false, which the rule already permitted when the key was present.
//
// +kubebuilder:validation:XValidation:rule="!has(self.preemption) || !self.preemption || (has(self.priorityClass) && size(self.priorityClass) > 0)",message="priorityClass is required when preemption is true"
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

	// OwnedPriorityClass is the name of the cluster-scoped PriorityClass this
	// SchedulingPolicy created and is therefore allowed to update and delete.
	// Empty means this SchedulingPolicy owns no PriorityClass, whatever
	// spec.priorityClass says and whatever any PriorityClass in the cluster
	// happens to be labelled with.
	//
	// This field, not a label, is what ownership means — the same reading
	// FrameService's binding uses for Secrets (see the "Ownership: a record,
	// not a label" note in internal/controller/services/binding.go). The
	// reason is the same and it is not theoretical here: the two
	// PriorityClasses this controller took over on the live cluster now carry
	// frame.plume-labs.io/policy-name themselves, written during the takeover,
	// so a label check would read its own footprint back as proof and keep
	// the takeover. A status subresource is writable only by the controller's
	// RBAC — schedulingpolicy_editor_role grants get and nothing else on
	// schedulingpolicies/status — so a namespaced editor who can create a
	// SchedulingPolicy naming any cluster-scoped PriorityClass cannot also
	// forge the record that would let this controller act on it.
	//
	// New in v1beta1 and deliberately not projected onto v1alpha1: v1alpha1
	// has no such field, and a v1alpha1 round trip that dropped it would only
	// ever make the controller disown a PriorityClass, never adopt one.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	OwnedPriorityClass string `json:"ownedPriorityClass,omitempty"`
}

// This is the conversion hub and the storage version. The marker arrived here
// in the same change that turned the conversion webhook on; see the note on
// the v1alpha1 FrameJob for why the two could not be separated.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
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
