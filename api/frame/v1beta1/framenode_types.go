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

// NetworkSpec defines network configuration for a node.
type NetworkSpec struct {
	// Address is the node's post-provisioning address. It is expected in CIDR
	// form — every stored object holds one (192.0.2.201/24) — which is why
	// it carries no isIP rule, in either direction: a conversion or a later
	// tightening that applies isIP here would strand all three.
	//
	// The format is *documented and deliberately not validated*, and the
	// MaxLength is deliberately the only bound. Leaving it wholly unbounded
	// was not acceptable past the freeze; but the field is named
	// "address" while every value is a prefix, nothing in the code enforces
	// either reading, and a pattern would freeze a decision nobody has taken.
	// A bound cannot strand anything and can be raised later; a pattern could
	// do neither. This note exists so the next reader does not add the
	// pattern believing it was merely overlooked — it was considered and
	// declined (Fix Round 1).
	//
	// 45 matches spec.ip and the network.dns items for no reason beyond
	// consistency; the longest expanded IPv6 prefix is 43 characters and the
	// stored values are 18.
	// +optional
	// +kubebuilder:validation:MaxLength=45
	Address string `json:"address,omitempty"`

	// Gateway is the default gateway for the node.
	// +optional
	Gateway string `json:"gateway,omitempty"`

	// DNS lists the resolvers for the node. The MaxLength on the items is
	// what lets the CEL cost estimator bound isIP(); without it the rule is
	// rejected as unboundedly expensive.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="self.all(x, isIP(x))",message="each network.dns entry must be a valid IP"
	DNS []string `json:"dns,omitempty"`

	// VLAN tags the node's interface.
	// +optional
	VLAN *int32 `json:"vlan,omitempty"`

	// Bond names the bonding interface to configure.
	// +optional
	Bond *string `json:"bond,omitempty"`
}

// FrameNodeSpec defines the desired state of FrameNode.
//
// It used to carry a spec-level CEL rule tying spec.disk to a complete
// network block: the gate of the Talos provisioning path. That path is gone,
// and spec.disk with it; none of the stored FrameNodes ever set it.
type FrameNodeSpec struct {
	// IP address of the node.
	//
	// Validated by CEL, not by Format. v1alpha1 carried a
	// validation:Format=ip marker, which does nothing: `ip` is not a
	// recognised OpenAPI format — the apiserver knows ipv4, ipv6 and cidr —
	// and unrecognised formats are silently dropped, so the only thing
	// rejecting a malformed address was the Go webhook's net.ParseIP. The
	// MaxLength is what lets the CEL cost estimator bound isIP(), the same
	// way network.dns needed it (T2).
	//
	// isIP() is not net.ParseIP: measured against the pinned
	// k8s.io/apiserver CEL library, it rejects IPv4-mapped IPv6
	// (::ffff:192.168.100.228) and zoned addresses (fe80::1%eth0), both of
	// which net.ParseIP accepts. So this is a real tightening over the
	// webhook, not just a relocation of it. All three stored FrameNodes hold
	// plain IPv4, so none is stranded. It also means 45 is unreachable — the
	// longest string isIP() accepts is a 39-character expanded IPv6 — but the
	// bound stays at 45 to match network.dns's items and because its job is
	// bounding CEL cost, not describing the value.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="ip must be a valid IP address"
	IP string `json:"ip"`

	// Role of the node in the cluster. Empty until classified.
	// +kubebuilder:validation:Enum=controlplane;worker;""
	// +optional
	Role string `json:"role,omitempty"`

	// Network configuration, descriptive only: Frame installs machines through
	// FrameInstall, not through FrameNode.
	// +optional
	Network NetworkSpec `json:"network,omitempty"`

	// RDMAInterface names the RDMA device (e.g. ib0, mlx5_0). Its presence is
	// projected onto the Kubernetes Node as frame.plume-labs.io/rdma=true.
	// +optional
	RDMAInterface string `json:"rdmaInterface,omitempty"`

	// Hostname for the node.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Hostname string `json:"hostname,omitempty"`

	// Rack identifier for topology. Projected onto the Kubernetes Node as the
	// label frame.plume-labs.io/rack, which is why it is bounded like a label
	// value: v1alpha1 accepted anything at all, and the controller then wrote
	// it as a label, so an over-long or malformed rack would be admitted here
	// and rejected there, leaving the FrameNode permanently short of Online
	// behind an error naming label validation rather than this field (T1).
	//
	// The bound is prophylactic, not a fix for an observed outage: every
	// stored FrameNode uses "rack-01", which passes. What the bound buys is that the
	// failure can no longer be introduced, and after the freeze it could
	// never be added.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$"
	Rack string `json:"rack,omitempty"`

	// Zone identifier for topology. Projected onto the Kubernetes Node as
	// topology.kubernetes.io/zone; bounded for the same reason as Rack.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$"
	Zone string `json:"zone,omitempty"`

	// ServiceClass is the tier of hardware this node offers, projected onto
	// the Kubernetes Node as frame.plume-labs.io/service-class and read by
	// the inference provider's NodeSelector.
	//
	// Optional and deliberately undefaulted: defaulting it would classify
	// hardware nobody has looked at. v1alpha1's enum also admitted the empty string; that member
	// is gone, because absence already means unclassified. Dropping it is
	// field-level, so ratcheting protects stored objects — and none of the
	// three that exist uses it.
	// +optional
	ServiceClass ServiceClass `json:"serviceClass,omitempty"`
}

// FrameNodeStatus defines the observed state of FrameNode.
//
// No status.phase (F2). The Ready condition's reason carries it: Online,
// Degraded or Offline once the node is in the cluster, or why it cannot be
// classified yet.
//
// For FrameNode this is not a cosmetic removal. framenode_controller.go reads
// Status.Phase as a state-machine *input*, not just as a report, which is why
// Part 0's Task 2 had to make setCondition correct first. Task 20 repoints
// that controller onto the Ready condition's reason; this type is the shape it
// moves onto.
type FrameNodeStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the FrameNode resource.
	// Ready's reason is the node's lifecycle state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// KubeletVersion is the kubelet version running on the node.
	// +optional
	KubeletVersion string `json:"kubeletVersion,omitempty"`

	// Capacity represents the total resources of the node.
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Allocatable represents the resources available for scheduling.
	// +optional
	Allocatable corev1.ResourceList `json:"allocatable,omitempty"`

	// NodeName is the Kubernetes node name.
	// +optional
	NodeName string `json:"nodeName,omitempty"`
}

// This is the conversion hub and the storage version. The marker arrived here
// in the same change that turned the conversion webhook on; see the note on
// the v1alpha1 FrameNode for why the two could not be separated.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fn
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
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
