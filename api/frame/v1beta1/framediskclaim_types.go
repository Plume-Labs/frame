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

// FrameDiskClaimSpec claims one physical disk for one purpose, destroying
// whatever is on it. It is modelled on FrameInstall: a one-shot act with a
// terminal phase, not a long-lived declaration that gets reconciled back
// into place. Re-running it means creating a new object.
type FrameDiskClaimSpec struct {
	// MachineRef names the FrameMachine whose disk this is.
	MachineRef LocalObjectReference `json:"machineRef"`

	// ByIDPath is the /dev/disk/by-id path of the disk. Never an sdX name:
	// on the machine this design was written against, sdX ordering changed
	// on all three boots, so a claim naming sdb would have wiped a
	// different disk on each one.
	// +kubebuilder:validation:Pattern=`^/dev/disk/by-id/.+`
	// +kubebuilder:validation:MaxLength=512
	ByIDPath string `json:"byIDPath"`

	// Serial is the disk's serial number, retyped by hand. It is matched
	// against what the node's agent reported, and the match is the
	// confirmation: a path can be stale, a serial the operator read off
	// the object they intend to destroy cannot.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Serial string `json:"serial"`

	// Destination is what the disk becomes. LVM and manual partitioning
	// are deliberately absent: each is a different destructive operation
	// with its own failure modes, and this lot ships the two that the park
	// actually needs.
	// +kubebuilder:validation:Enum=wipe;ceph-osd
	Destination string `json:"destination"`
}

type LocalObjectReference struct {
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

type FrameDiskClaimStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Pending;Claiming;Ready;Failed
	Phase string `json:"phase,omitempty"`

	// ClaimUID is written onto the machine before the destructive work
	// begins and read back before it is repeated. It lives on the node, not
	// in this controller's memory: a manager restart replayed an entire
	// destructive sequence in the previous lot because the only record that
	// it had already run was process-local.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	ClaimUID string `json:"claimUID,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// +optional
	PhaseSince *metav1.Time `json:"phaseSince,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fdc
// +kubebuilder:printcolumn:name="Machine",type=string,JSONPath=".spec.machineRef.name"
// +kubebuilder:printcolumn:name="Serial",type=string,JSONPath=".spec.serial"
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=".spec.destination"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameDiskClaim is the Schema for the framediskclaims API.
type FrameDiskClaim struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec FrameDiskClaimSpec `json:"spec,omitempty"`
	// +optional
	Status FrameDiskClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameDiskClaimList contains a list of FrameDiskClaim.
type FrameDiskClaimList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameDiskClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameDiskClaim{}, &FrameDiskClaimList{})
}
