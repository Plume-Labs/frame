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

// NetworkHealthPolicyName is the only name the kind accepts.
const NetworkHealthPolicyName = "default"

// DefaultMinLinkSpeedMbps is what a link that is up must reach, with no policy
// or no expectation for it.
const DefaultMinLinkSpeedMbps int32 = 1000

// ExpectedLink is what one node's interface should be. Naming a link makes it
// required: it raises NetworkDegraded when absent or down, which an unused port
// on a machine with no expectation never does.
type ExpectedLink struct {
	// Node is the FrameMachine's spec.nodeRef.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Node string `json:"node"`
	// Interface is the kernel name, a physical port or a bond.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=15
	Interface string `json:"interface"`
	// SpeedMbps replaces MinLinkSpeedMbps for this link.
	// +kubebuilder:validation:Minimum=1
	// +optional
	SpeedMbps int32 `json:"speedMbps,omitempty"`
	// Members is how many members a bond must hold: a member that left the
	// bond altogether is in no list, so only this number can miss it.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	Members int32 `json:"members,omitempty"`
}

// NetworkHealthPolicySpec is what the machine controller compares a node's
// links against. What counts as a bond member out is not a setting.
type NetworkHealthPolicySpec struct {
	// MinLinkSpeedMbps raises LinkSpeedLow on a physical link that is up below it.
	// +kubebuilder:default=1000
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinLinkSpeedMbps int32 `json:"minLinkSpeedMbps,omitempty"`

	// Links are the expectations for named links.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Links []ExpectedLink `json:"links,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the policy is a singleton named default"
// +kubebuilder:printcolumn:name="Min speed (Mb/s)",type=integer,JSONPath=`.spec.minLinkSpeedMbps`

// NetworkHealthPolicy holds what FrameMachine's network conditions and alerts
// expect. Its absence means the default. It has no status: the machine
// controller reads it on each pass.
type NetworkHealthPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec NetworkHealthPolicySpec `json:"spec"`
}

// +kubebuilder:object:root=true

// NetworkHealthPolicyList contains a list of NetworkHealthPolicy.
type NetworkHealthPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkHealthPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkHealthPolicy{}, &NetworkHealthPolicyList{})
}
