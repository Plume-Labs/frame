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
	// +optional
	Address string `json:"address,omitempty"`
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +optional
	// +kubebuilder:validation:items:Format=ip
	DNS []string `json:"dns,omitempty"`
	// +optional
	VLAN *int32 `json:"vlan,omitempty"`
	// +optional
	Bond *string `json:"bond,omitempty"`
}

// DiskInfo describes a disk discovered on a node in maintenance mode
type DiskInfo struct {
	Name string `json:"name"`
	Size string `json:"size"`
	Type string `json:"type"`
}

// NICInfo describes a network interface discovered on a node in maintenance mode
type NICInfo struct {
	Name  string `json:"name"`
	MAC   string `json:"mac"`
	Speed string `json:"speed"`
}

// FrameNodeSpec defines the desired state of FrameNode
//
// +kubebuilder:validation:XValidation:rule="!has(self.disk) || size(self.disk) == 0 || (has(self.network) && has(self.network.address) && size(self.network.address) > 0 && has(self.network.gateway) && size(self.network.gateway) > 0 && has(self.network.dns) && self.network.dns.size() > 0)",message="network.address, network.gateway, and at least one network.dns entry are required once disk is set"
type FrameNodeSpec struct {
	// IP address of the node in maintenance mode
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=ip
	IP string `json:"ip"`

	// Role of the node in the cluster. Empty during initial discovery phase.
	// +kubebuilder:validation:Enum=controlplane;worker;""
	// +optional
	Role string `json:"role,omitempty"`

	// Network configuration. Set after discovery to trigger provisioning.
	// +optional
	Network NetworkSpec `json:"network,omitempty"`

	// Disk device for Talos installation (e.g., /dev/nvme0n1). Set after discovery.
	// +optional
	Disk string `json:"disk,omitempty"`

	// RDMA interface name (e.g., ib0, mlx5_0)
	// +optional
	RDMAInterface string `json:"rdmaInterface,omitempty"`

	// Hostname for the node
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Hostname string `json:"hostname,omitempty"`

	// Rack identifier for topology
	// +optional
	Rack string `json:"rack,omitempty"`

	// Zone identifier for topology
	// +optional
	Zone string `json:"zone,omitempty"`

	// Service class for workload placement
	// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW;""
	// +optional
	ServiceClass string `json:"serviceClass,omitempty"`
}

// FrameNodeStatus defines the observed state of FrameNode.
type FrameNodeStatus struct {
	// Current phase of the node
	// +kubebuilder:validation:Enum=Discovering;Discovered;Provisioning;Online;Degraded;Offline;Failed
	Phase string `json:"phase,omitempty"`

	// DiscoveredHostname is the hostname reported by the node in maintenance mode
	// +optional
	DiscoveredHostname string `json:"discoveredHostname,omitempty"`

	// DiscoveredTalosVersion is the Talos version reported during maintenance mode discovery
	// +optional
	DiscoveredTalosVersion string `json:"discoveredTalosVersion,omitempty"`

	// DiscoveredDisks contains disk information from maintenance mode discovery
	// +optional
	DiscoveredDisks []DiskInfo `json:"discoveredDisks,omitempty"`

	// DiscoveredNICs contains network interface information from maintenance mode discovery
	// +optional
	DiscoveredNICs []NICInfo `json:"discoveredNICs,omitempty"`

	// Conditions represent the current state of the FrameNode resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Kubelet version running on the node
	// +optional
	KubeletVersion string `json:"kubeletVersion,omitempty"`

	// Capacity represents the total resources of the node
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Allocatable represents the resources available for scheduling
	// +optional
	Allocatable corev1.ResourceList `json:"allocatable,omitempty"`

	// NodeName is the Kubernetes node name
	// +optional
	NodeName string `json:"nodeName,omitempty"`
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
