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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// TalosSecretReference names the Secret holding Talos client certificates.
// Mirrors corev1.SecretReference's wire shape (name + namespace) as a local
// type so Namespace can carry a validation marker directly: a kubebuilder
// marker cannot be attached to a subfield of an external k8s.io/api type,
// and the CEL XValidation equivalent (self.namespace.matches(...)) blows the
// per-schema CEL cost budget because corev1.SecretReference.Namespace has no
// declared maxLength for the cost estimator to bound the regex against.
//
// +structType=atomic
type TalosSecretReference struct {
	// Name of the referenced Secret. Optional to match corev1.SecretReference,
	// which this type mirrors -- making it Required here would be a new
	// constraint arriving unremarked. Whether it should be required is
	// Phase B's to decide deliberately.
	// +optional
	Name string `json:"name,omitempty"`

	// Namespace of the referenced Secret. Empty means "this CR's own
	// namespace" -- buildTalosClient falls back to it explicitly -- so the
	// pattern accepts empty alongside a well-formed label. May reference a
	// namespace other than this CR's own: the controller's Secret RBAC is
	// cluster-wide, so that reach is deliberately unconstrained today --
	// Phase B's RBAC-tier lock-down is where it gets settled, not here.
	// This only rejects a malformed value, not a cross-namespace one.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^([a-z0-9]([-a-z0-9]*[a-z0-9])?)?$"
	Namespace string `json:"namespace,omitempty"`
}

// TalosMachineConfigSpec defines the desired state of TalosMachineConfig
//
// +kubebuilder:validation:XValidation:rule="(has(self.configPatch) && size(self.configPatch) > 0) != has(self.configPatchRef)",message="exactly one of configPatch or configPatchRef must be set"
type TalosMachineConfigSpec struct {
	// NodeName is the Kubernetes node name to apply the config patch to.
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

	// ConfigPatch is an inline Talos config patch document (YAML).
	// +optional
	ConfigPatch string `json:"configPatch,omitempty"`

	// ConfigPatchRef references a ConfigMap containing the patch under key "patch.yaml".
	// +optional
	ConfigPatchRef *corev1.ConfigMapKeySelector `json:"configPatchRef,omitempty"`
}

// TalosMachineConfigStatus defines the observed state of TalosMachineConfig.
type TalosMachineConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the TalosMachineConfig resource.
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
