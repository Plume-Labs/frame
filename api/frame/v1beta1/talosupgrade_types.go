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

// TalosUpgradeSpec defines the desired state of TalosUpgrade.
//
// No spec-level XValidation, on either version: the one rule this kind has
// bounds a single field and is expressed on that field, where ratcheting can
// reach it.
type TalosUpgradeSpec struct {
	// NodeName is the Kubernetes node name to upgrade.
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
	// certificates, in this CR's own namespace. See TalosSecretReference in
	// talosmachineconfig_types.go for why it no longer names one.
	// +kubebuilder:validation:Required
	TalosSecretRef TalosSecretReference `json:"talosSecretRef"`

	// Image is the Talos installer image to upgrade to
	// (e.g. ghcr.io/siderolabs/installer:v1.8.0).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="self.split('/')[self.split('/').size()-1].contains(':')",message="image must include a tag (e.g. installer:v1.8.0)"
	Image string `json:"image"`
}

// TalosUpgradeStatus defines the observed state of TalosUpgrade.
//
// No status.phase (F2): this kind never had one. Every field here already
// exists on v1alpha1 — Part 0's Task 6 added observedGeneration there first,
// deliberately, so that v1beta1 has no field v1alpha1 lacks and Task 18's
// conversion functions need no annotation escape hatch.
type TalosUpgradeStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the TalosUpgrade resource.
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
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// TalosUpgrade is the Schema for the talosupgrades API
type TalosUpgrade struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of TalosUpgrade
	// +required
	Spec TalosUpgradeSpec `json:"spec"`

	// status defines the observed state of TalosUpgrade
	// +optional
	Status TalosUpgradeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TalosUpgradeList contains a list of TalosUpgrade
type TalosUpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosUpgrade `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosUpgrade{}, &TalosUpgradeList{})
}
