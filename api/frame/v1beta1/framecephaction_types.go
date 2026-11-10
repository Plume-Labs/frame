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

// FrameCephActionSpec is one everyday Ceph gesture from the console: mark
// an OSD in or out, restart it, scrub or deep-scrub it, or set a cluster
// flag for a while. Every gesture is reversible and none destroys data;
// the manager sends it through the Ceph operator account
// (docs/ceph-actions.md). Like FrameOSDReplacement it is a one-shot object:
// doing it again means a new one.
//
// +kubebuilder:validation:XValidation:rule="self.action == oldSelf.action && has(self.osdID) == has(oldSelf.osdID) && (!has(self.osdID) || self.osdID == oldSelf.osdID) && has(self.flag) == has(oldSelf.flag) && (!has(self.flag) || self.flag == oldSelf.flag) && has(self.durationMinutes) == has(oldSelf.durationMinutes) && (!has(self.durationMinutes) || self.durationMinutes == oldSelf.durationMinutes)",message="the gesture is fixed at creation; only release may change"
// +kubebuilder:validation:XValidation:rule="self.action == 'SetFlag' ? (has(self.flag) && !has(self.osdID)) : (has(self.osdID) && !has(self.flag) && !has(self.durationMinutes))",message="SetFlag takes a flag (and a duration); every other action takes an osdID"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.release) || !oldSelf.release || (has(self.release) && self.release)",message="release cannot be taken back"
type FrameCephActionSpec struct {
	// Action is the gesture.
	// +kubebuilder:validation:Enum=OSDIn;OSDOut;OSDRestart;OSDScrub;OSDDeepScrub;SetFlag
	Action string `json:"action"`

	// OSDID is the OSD acted on (every action but SetFlag).
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	OSDID *int32 `json:"osdID,omitempty"`

	// Flag is the cluster flag set by SetFlag. The manager unsets it when
	// the duration has passed, on release, or if the object is deleted,
	// unless it was already set before this action (then it is left set).
	// One active FrameCephAction holds a flag at a time.
	// +optional
	// +kubebuilder:validation:Enum=noout;norebalance;nobackfill;norecover;noscrub;nodeep-scrub
	Flag string `json:"flag,omitempty"`

	// DurationMinutes is how long the flag stays set (SetFlag only; 120
	// when left out).
	// +optional
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:validation:Maximum=1440
	DurationMinutes int32 `json:"durationMinutes,omitempty"`

	// Release unsets a SetFlag's flag before its time (same rule as at the
	// end of the duration).
	// +optional
	Release bool `json:"release,omitempty"`
}

// FrameCephAction phases.
const (
	// CephActionPending: not sent yet (waiting for the account or for a
	// check that may pass later).
	CephActionPending = "Pending"
	// CephActionActive: a SetFlag whose flag is set, until its end.
	CephActionActive = "Active"
	// CephActionDone: the gesture was sent and Ceph accepted it (for a
	// SetFlag: the flag is no longer held by this object).
	CephActionDone = "Done"
	// CephActionRefused: a check refused the gesture; nothing was sent.
	CephActionRefused = "Refused"
	// CephActionFailed: the gesture was sent and failed, or its answer was
	// lost; it is never sent again.
	CephActionFailed = "Failed"
)

// FrameCephAction actions.
const (
	CephActionOSDIn        = "OSDIn"
	CephActionOSDOut       = "OSDOut"
	CephActionOSDRestart   = "OSDRestart"
	CephActionOSDScrub     = "OSDScrub"
	CephActionOSDDeepScrub = "OSDDeepScrub"
	CephActionSetFlag      = "SetFlag"
)

// FrameCephActionStatus is what the manager did.
type FrameCephActionStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Pending;Active;Done;Refused;Failed
	Phase string `json:"phase,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// SentAt is written before the gesture is sent: once it is there the
	// gesture is never sent again by this object.
	// +optional
	SentAt *metav1.Time `json:"sentAt,omitempty"`

	// DoneAt is when the gesture was confirmed.
	// +optional
	DoneAt *metav1.Time `json:"doneAt,omitempty"`

	// ExpiresAt is when a SetFlag's flag is unset.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// FlagWasSet: the flag was already set when this SetFlag started; it is
	// then left set at the end (it was not this object's).
	// +optional
	FlagWasSet bool `json:"flagWasSet,omitempty"`

	// UnsetSentAt is when the manager first tried to unset the flag; it
	// retries until Ceph accepts it.
	// +optional
	UnsetSentAt *metav1.Time `json:"unsetSentAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fca
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=".spec.action"
// +kubebuilder:printcolumn:name="OSD",type=integer,JSONPath=".spec.osdID"
// +kubebuilder:printcolumn:name="Flag",type=string,JSONPath=".spec.flag"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=".status.expiresAt"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameCephAction is the Schema for the framecephactions API.
type FrameCephAction struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Spec is required: a missing spec would skip its validation rules.
	// +required
	Spec FrameCephActionSpec `json:"spec"`
	// +optional
	Status FrameCephActionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameCephActionList contains a list of FrameCephAction.
type FrameCephActionList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameCephAction `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameCephAction{}, &FrameCephActionList{})
}
