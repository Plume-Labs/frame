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

// TalosUpgradeSpec defines the desired state of TalosUpgrade
type TalosUpgradeSpec struct {
	// NodeName is the Kubernetes node name to upgrade.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	NodeName string `json:"nodeName"`

	// TalosEndpoint is the Talos API endpoint (host:port) for this node.
	// The bracketed form (e.g. [fd00::1]:50000) is accepted for IPv6, same
	// as net.SplitHostPort in the webhook check this mirrors.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^(\\[[0-9a-fA-F:]+\\]|[a-zA-Z0-9.-]+):[0-9]+$"
	TalosEndpoint string `json:"talosEndpoint"`

	// TalosSecretRef references the Secret containing Talos client certificates
	// (keys: ca.crt, client.crt, client.key).
	// +kubebuilder:validation:Required
	TalosSecretRef TalosSecretReference `json:"talosSecretRef"`

	// Image is the Talos installer image to upgrade to (e.g. ghcr.io/siderolabs/installer:v1.8.0).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="self.split('/')[self.split('/').size()-1].contains(':')",message="image must include a tag (e.g. installer:v1.8.0)"
	Image string `json:"image"`
}

// TalosUpgradeStatus defines the observed state of TalosUpgrade.
type TalosUpgradeStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the TalosUpgrade resource.
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
// — see the note on the v1alpha1 FrameJob for the general reasoning.
//
// This kind has one field v1beta1 lacks: talosSecretRef.namespace, removed by
// F6. Conversion does not stash it, deliberately — ConvertFrom returns it
// empty because empty already meant "this CR's own namespace", which is what
// the controller has always done, so the normalised value is the truth rather
// than a placeholder. The consequence of the storage version moving is
// therefore real and intended: a v1alpha1 client may still *write*
// talosSecretRef.namespace, but no longer reads it back. That is what the
// deprecation warning on this version has said all along — the field is
// ignored — now made literal.
//
// +kubebuilder:object:root=true
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 TalosUpgrade is deprecated; use frame.plume-labs.io/v1beta1. talosSecretRef.namespace is ignored — the Secret is read from this CR's own namespace — and talosSecretRef.name is now required."
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
