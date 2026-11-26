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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	UpdatePhasePending     = "Pending"
	UpdatePhaseVerifying   = "Verifying"
	UpdatePhaseApplying    = "Applying"
	UpdatePhaseSucceeded   = "Succeeded"
	UpdatePhaseRollingBack = "RollingBack"
	UpdatePhaseRolledBack  = "RolledBack"
	UpdatePhaseRefused     = "Refused"
	UpdatePhaseFailed      = "Failed"
)

// FrameUpdateSpec is a request to move Hearthrack to another version, or to
// go back to the previous one. It is the only surface the console touches to
// update anything (docs/specs/2026-11-24-mise-a-jour-en-un-clic.md §3.3): the
// console never patches a Deployment or an image itself.
//
// The request is a gesture, fixed at creation. Changing the target of an
// update already running would make "what was asked" unreadable afterwards.
//
// There is deliberately no field to skip signature verification. Force only
// lifts the "something is in flight" check; a signature failure is final.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="an update request is fixed at creation"
// +kubebuilder:validation:XValidation:rule="(has(self.rollback) && self.rollback) ? !has(self.targetVersion) : has(self.targetVersion)",message="set targetVersion, or rollback, not both and not neither"
type FrameUpdateSpec struct {
	// TargetVersion is the FrameRelease version to move to.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
	// +kubebuilder:validation:MaxLength=64
	TargetVersion string `json:"targetVersion,omitempty"`

	// Rollback asks to return to the version this install ran before its last
	// successful update, when that release says it is safe.
	// +optional
	Rollback bool `json:"rollback,omitempty"`

	// Force proceeds although jobs, installs or components are not at rest. It
	// never overrides a signature check, and never overrides an OSD
	// replacement in progress.
	// +optional
	Force bool `json:"force,omitempty"`
}

// UpdateComponentStatus is one component's progress through an update.
type UpdateComponentStatus struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// From and To are image references pinned by digest.
	// +optional
	From string `json:"from,omitempty"`
	// +optional
	To string `json:"to,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Applying;Ready;RolledBack;Failed
	// +optional
	State string `json:"state,omitempty"`
}

// Updater states, in the order they happen.
const (
	UpdaterPending     = "Pending"
	UpdaterApplying    = "Applying"
	UpdaterRollingBack = "RollingBack"
	UpdaterReady       = "Ready"
	UpdaterRolledBack  = "RolledBack"
	UpdaterFailed      = "Failed"
)

// UpdaterStatus is the self-update of the updater: the one component the
// updater does not apply itself, because the process that drives the update
// would disappear in the middle of it. The updater asks (To, Pending); the
// operator applies the image, watches it become ready, and puts From back if it
// does not (spec section 4.1, cross supervision).
type UpdaterStatus struct {
	// To is the updater image the release names, pinned by digest. Written by
	// the updater.
	// +kubebuilder:validation:MaxLength=512
	To string `json:"to"`
	// From is the image the updater ran before; written by the operator.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	From string `json:"from,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Applying;RollingBack;Ready;RolledBack;Failed
	// +optional
	State string `json:"state,omitempty"`
	// RequestedAt is when the updater asked. A request the operator has not
	// settled long after it was made is given up on by the updater.
	// +optional
	RequestedAt *metav1.Time `json:"requestedAt,omitempty"`
	// Since is when the operator started applying To.
	// +optional
	Since *metav1.Time `json:"since,omitempty"`
	// ReadySince is when the new updater was first seen ready and still is: it
	// must stay so for a while before the update is called done.
	// +optional
	ReadySince *metav1.Time `json:"readySince,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
}

// FrameUpdateStatus is written by the updater, never by the console.
type FrameUpdateStatus struct {
	// +kubebuilder:validation:Enum=Pending;Verifying;Applying;Succeeded;RollingBack;RolledBack;Refused;Failed
	// +optional
	Phase string `json:"phase,omitempty"`

	// Step is the human-readable step in progress ("Applying uiproxy").
	// +optional
	// +kubebuilder:validation:MaxLength=200
	Step string `json:"step,omitempty"`

	// FromVersion is the version running when the update began. It is what a
	// later rollback returns to.
	// +optional
	FromVersion string `json:"fromVersion,omitempty"`

	// Components lists each component and where it stands, in apply order.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	Components []UpdateComponentStatus `json:"components,omitempty"`

	// Message says why an update was refused, failed or rolled back.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Updater is the updater's own update, applied by the operator.
	// +optional
	Updater *UpdaterStatus `json:"updater,omitempty"`

	// RequestedBy is the Kubernetes user the request was impersonated as, copied
	// by the updater for the journal.
	// +optional
	// +kubebuilder:validation:MaxLength=254
	RequestedBy string `json:"requestedBy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fupd
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=".spec.targetVersion"
// +kubebuilder:printcolumn:name="Rollback",type=boolean,JSONPath=".spec.rollback"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Step",type=string,JSONPath=".status.step"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:storageversion

// FrameUpdate is a request to update, or roll back, Hearthrack itself.
type FrameUpdate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec   FrameUpdateSpec   `json:"spec"`
	Status FrameUpdateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameUpdateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameUpdate `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameUpdate{}, &FrameUpdateList{}) }
