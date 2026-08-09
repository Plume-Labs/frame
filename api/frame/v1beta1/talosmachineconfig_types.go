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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TalosSecretReference names the Secret holding Talos client certificates
// (keys: ca.crt, client.crt, client.key), in the referring CR's own
// namespace.
//
// v1alpha1 had a Namespace field here, and it reached anywhere: the manager
// holds cluster-wide `get secrets`, so a CR in a namespace the caller
// controls could make the operator read Talos client certificates — node
// root credentials — out of any namespace, and any failure surfacing the
// Secret's contents in a condition or an Event would exfiltrate them. It is
// gone (F6). The rule: no field in a Frame spec may name a namespace the CR
// does not live in, except through a mechanism that records what it wrote
// and refuses to touch anything it did not create.
//
// Name is Required (F7). v1alpha1 left it optional to mirror
// corev1.SecretReference, which is optional because it is a general-purpose
// type used where a name may legitimately be absent. This is not one of
// those contexts: there is no code path where an unnamed Talos secret means
// anything, and leaving it optional meant buildTalosClient looked up a
// Secret named "" and failed at reconcile time with a condition rather than
// at admission.
//
// The type stays local rather than becoming corev1.LocalObjectReference,
// which it now structurally matches: a kubebuilder marker cannot be attached
// to a subfield of an external k8s.io/api type, so Name could not be made
// Required there — the same limitation that created this type.
//
// +structType=atomic
type TalosSecretReference struct {
	// Name of the referenced Secret, in this CR's own namespace.
	//
	// Two of the three markers below are belt and braces, measured rather
	// than assumed. Required restates controller-gen's default — in this
	// version a field is required unless it carries +optional, and the json
	// tag's omitempty has no bearing on it — so stripping Required changes no
	// byte of the generated schema; the mutation that moves requiredness is
	// Required -> +optional. MinLength=1 and Pattern each reject the empty
	// string on their own, so removing either alone changes nothing
	// observable; removing both does. Both are kept because each states an
	// intent the other does not, and either could outlive the other.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Name string `json:"name"`
}

// TalosMachineConfigSpec defines the desired state of TalosMachineConfig.
//
// The spec-level rule below is inherited from v1alpha1 unchanged and is one
// of the four object-level CEL rules the freeze makes permanent. Object-level
// rules are the ones ratcheting cannot save you from — it is per-schema-node,
// so an over-strict rule here permanently freezes a stored object — which is
// why this one is carried over verbatim and no new one is added. Zero
// TalosMachineConfigs exist on any cluster, so it strands nothing by
// construction.
//
// +kubebuilder:validation:XValidation:rule="(has(self.configPatch) && size(self.configPatch) > 0) != has(self.configPatchRef)",message="exactly one of configPatch or configPatchRef must be set"
type TalosMachineConfigSpec struct {
	// NodeName is the Kubernetes node name to apply the config patch to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	NodeName string `json:"nodeName"`

	// TalosEndpoint is the Talos API endpoint (host:port) for this node. The
	// bracketed form (e.g. [fd00::1]:50000) is accepted for IPv6, same as
	// net.SplitHostPort in the webhook check this mirrors.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^(\\[[0-9a-fA-F:]+\\]|[a-zA-Z0-9.-]+):[0-9]+$"
	TalosEndpoint string `json:"talosEndpoint"`

	// TalosSecretRef references the Secret containing Talos client
	// certificates, in this CR's own namespace.
	// +kubebuilder:validation:Required
	TalosSecretRef TalosSecretReference `json:"talosSecretRef"`

	// ConfigPatch is an inline Talos config patch document (YAML).
	// +optional
	ConfigPatch string `json:"configPatch,omitempty"`

	// ConfigPatchRef references a ConfigMap containing the patch under key
	// "patch.yaml".
	// +optional
	ConfigPatchRef *corev1.ConfigMapKeySelector `json:"configPatchRef,omitempty"`
}

// TalosMachineConfigStatus defines the observed state of TalosMachineConfig.
//
// No status.phase (F2): this kind never had one. Every field here already
// exists on v1alpha1 — Part 0's Task 6 added observedGeneration there first,
// deliberately, so that v1beta1 has no field v1alpha1 lacks and Task 18's
// conversion functions need no annotation escape hatch.
type TalosMachineConfigStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary. The controller also guards its
	// success path on it (Part 0's Task 2): without that guard the first
	// successful reconcile re-ran ApplyConfiguration against a live machine.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the TalosMachineConfig
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
// +kubebuilder:printcolumn:name="NodeName",type=string,JSONPath=".spec.nodeName"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// TalosMachineConfig is the Schema for the talosmachineconfigs API
type TalosMachineConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of TalosMachineConfig
	// +required
	Spec TalosMachineConfigSpec `json:"spec"`

	// status defines the observed state of TalosMachineConfig
	// +optional
	Status TalosMachineConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TalosMachineConfigList contains a list of TalosMachineConfig
type TalosMachineConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosMachineConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosMachineConfig{}, &TalosMachineConfigList{})
}
