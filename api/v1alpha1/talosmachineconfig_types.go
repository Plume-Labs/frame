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

// TalosMachineConfigSpec defines the desired state of TalosMachineConfig
type TalosMachineConfigSpec struct {
	// NodeName is the Kubernetes node name to apply the config patch to.
	// +kubebuilder:validation:Required
	NodeName string `json:"nodeName"`

	// TalosEndpoint is the Talos API endpoint (host:port) for this node.
	// +kubebuilder:validation:Required
	TalosEndpoint string `json:"talosEndpoint"`

	// TalosSecretRef references the Secret containing Talos client certificates
	// (keys: ca.crt, client.crt, client.key).
	// +kubebuilder:validation:Required
	TalosSecretRef corev1.SecretReference `json:"talosSecretRef"`

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
