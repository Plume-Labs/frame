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

// NetworkSpec defines network configuration for a node
type NetworkSpec struct {
	Address string   `json:"address"`
	Gateway string   `json:"gateway"`
	DNS     []string `json:"dns"`
	VLAN    *int32   `json:"vlan,omitempty"`
	Bond    *string  `json:"bond,omitempty"`
}

// FrameNodeSpec defines the desired state of FrameNode
type FrameNodeSpec struct {
	// IP address of the node in maintenance mode
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=ip
	IP string `json:"ip"`

	// Role of the node in the cluster
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=controlplane;worker
	Role string `json:"role"`

	// Network configuration
	// +kubebuilder:validation:Required
	Network NetworkSpec `json:"network"`

	// Disk device for Talos installation (e.g., /dev/nvme0n1)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^/dev/"
	Disk string `json:"disk"`

	// RDMA interface name (e.g., ib0, mlx5_0)
	// +optional
	RDMAInterface string `json:"rdmaInterface,omitempty"`

	// Hostname for the node
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Hostname string `json:"hostname,omitempty"`

	// Rack identifier for topology
	// +kubebuilder:validation:Required
	Rack string `json:"rack"`

	// Zone identifier for topology
	// +kubebuilder:validation:Required
	Zone string `json:"zone"`

	// Service class for workload placement
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW
	ServiceClass string `json:"serviceClass"`

	// Reference to Sidero ServerClass for provisioning
	// +optional
	ServerClassRef *corev1.ObjectReference `json:"serverClassRef,omitempty"`
}

// FrameNodeStatus defines the observed state of FrameNode.
type FrameNodeStatus struct {
	// Current phase of the node
	// +kubebuilder:validation:Enum=Provisioning;Online;Degraded;Offline;Failed
	Phase string `json:"phase,omitempty"`

	// Conditions represent the current state of the FrameNode resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Talos version running on the node
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// Kubelet version running on the node
	// +optional
	KubeletVersion string `json:"kubeletVersion,omitempty"`

	// Last heartbeat received from the node
	// +optional
	LastHeartbeat *metav1.Time `json:"lastHeartbeat,omitempty"`

	// Capacity represents the total resources of the node
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Allocatable represents the resources available for scheduling
	// +optional
	Allocatable corev1.ResourceList `json:"allocatable,omitempty"`

	// NodeName is the Kubernetes node name
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// ProviderID is the cloud provider ID
	// +optional
	ProviderID string `json:"providerID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fn
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="ServiceClass",type=string,JSONPath=".spec.serviceClass"
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=".spec.zone"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameNode is the Schema for the framenodes API
type FrameNode struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of FrameNode
	// +required
	Spec FrameNodeSpec `json:"spec"`

	// status defines the observed state of FrameNode
	// +optional
	Status FrameNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameNodeList contains a list of FrameNode
type FrameNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameNode{}, &FrameNodeList{})
}
