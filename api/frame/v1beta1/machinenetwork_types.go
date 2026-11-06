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

// The node's links as the agent reads them from sysfs
// (internal/agent/network.go). Bounded like status.storage.local: one value
// past a marker gets the whole status write refused, so the agent clips, and
// says how much it left out.

// MachineNetwork is the node's physical ports and bonds.
type MachineNetwork struct {
	// ObservedAt is when the agent read them.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
	// Error says why nothing could be read; the lists are then empty.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Error string `json:"error,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Interfaces []NetworkInterface `json:"interfaces,omitempty"`
	// +optional
	InterfacesOmitted int32 `json:"interfacesOmitted,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Bonds []NetworkBond `json:"bonds,omitempty"`
	// +optional
	BondsOmitted int32 `json:"bondsOmitted,omitempty"`
}

// NetworkInterface is one physical port.
type NetworkInterface struct {
	// +kubebuilder:validation:MaxLength=15
	Name string `json:"name"`
	// OperState is the kernel's, verbatim: up, down, lowerlayerdown…
	// +optional
	// +kubebuilder:validation:MaxLength=32
	OperState string `json:"operState,omitempty"`
	// SpeedMbps is nil when the kernel gives none, which is every link that
	// is down.
	// +optional
	SpeedMbps *int32 `json:"speedMbps,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Duplex string `json:"duplex,omitempty"`
	// +optional
	MTU int32 `json:"mtu,omitempty"`
	// Master is the bond holding this port.
	// +optional
	// +kubebuilder:validation:MaxLength=15
	Master string `json:"master,omitempty"`
	// Counters since the driver loaded.
	// +optional
	RxErrors int64 `json:"rxErrors,omitempty"`
	// +optional
	TxErrors int64 `json:"txErrors,omitempty"`
	// +optional
	RxDropped int64 `json:"rxDropped,omitempty"`
	// +optional
	TxDropped int64 `json:"txDropped,omitempty"`
}

// NetworkBond is one bonding device.
type NetworkBond struct {
	// +kubebuilder:validation:MaxLength=15
	Name string `json:"name"`
	// Mode is the kernel's name, e.g. 802.3ad.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Mode string `json:"mode,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	MIIStatus string `json:"miiStatus,omitempty"`
	// +optional
	MTU int32 `json:"mtu,omitempty"`
	// AggregatorID is the active LACP aggregator; zero outside 802.3ad.
	// +optional
	AggregatorID int32 `json:"aggregatorID,omitempty"`
	// PortsInAggregator is how many ports LACP has in it.
	// +optional
	PortsInAggregator int32 `json:"portsInAggregator,omitempty"`
	// PartnerMAC is the switch's LACP system id; all zeros means no partner.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	PartnerMAC string `json:"partnerMAC,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Members []BondMember `json:"members,omitempty"`
	// +optional
	MembersOmitted int32 `json:"membersOmitted,omitempty"`
	// MembersUnread says the bond's list could not be read: an empty Members
	// then says nothing.
	// +optional
	MembersUnread bool `json:"membersUnread,omitempty"`
}

// BondMember is one port of a bond, as the bond sees it.
type BondMember struct {
	// +kubebuilder:validation:MaxLength=15
	Name string `json:"name"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	MIIStatus string `json:"miiStatus,omitempty"`
	// State is active or backup.
	// +optional
	// +kubebuilder:validation:MaxLength=16
	State string `json:"state,omitempty"`
	// +optional
	LinkFailures int64 `json:"linkFailures,omitempty"`
	// AggregatorID is the LACP aggregator this port joined; a port outside
	// the bond's own is up and carries nothing.
	// +optional
	AggregatorID int32 `json:"aggregatorID,omitempty"`
}
