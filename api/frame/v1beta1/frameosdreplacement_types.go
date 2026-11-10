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

// FrameOSDReplacementSpec replaces one Ceph OSD's disk by a new one, end to
// end: drain, purge, physical swap, wipe of the new disk, declaration to
// Rook, backfill. Like FrameDiskClaim it is a one-shot act with terminal
// phases; running it again means a new object.
//
// Two steps cannot be undone, and each waits for a human: the purge of the
// OSD (purgeApproval) and the wipe of the new disk (wipeApproval). Each is
// confirmed by retyping a serial number the person reads off the disk they
// mean, never by a click.
//
// +kubebuilder:validation:XValidation:rule="self.osdID == oldSelf.osdID && self.serial == oldSelf.serial",message="osdID and serial are fixed at creation"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cancel) || !oldSelf.cancel || (has(self.cancel) && self.cancel)",message="cancel cannot be taken back"
type FrameOSDReplacementSpec struct {
	// OSDID is the Ceph OSD to replace (7 for osd.7).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	OSDID int32 `json:"osdID"`

	// Serial is the serial number of the OSD's current disk, retyped at
	// launch. It must match the serial Ceph reports for this OSD: the
	// replacement starts only on the disk the person meant.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Serial string `json:"serial"`

	// PurgeApproval is approval no. 1: the old disk's serial, retyped once
	// the OSD is drained and safe-to-destroy. The purge is irreversible.
	// A serial that does not match is ignored and may be retyped; the one
	// accepted is dated in status.purgeApprovedAt, and only an approval
	// given while the replacement waits for it counts.
	// +optional
	PurgeApproval *SerialApproval `json:"purgeApproval,omitempty"`

	// WipeApproval is approval no. 2: the new disk's serial, read on its
	// label and retyped once Frame shows the disk it found. The wipe is
	// irreversible.
	// +optional
	WipeApproval *SerialApproval `json:"wipeApproval,omitempty"`

	// Cancel stops the replacement. Before the purge, a drained OSD is
	// marked in again; after it, nothing is undone and the status says
	// where things stand.
	// +optional
	Cancel bool `json:"cancel,omitempty"`

	// Resume is raised by one to retry the current step after a pause
	// (status.phase Paused). A destructive call whose outcome is unknown is
	// never sent again by a resume: that step pauses again and says what to
	// check by hand.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Resume int32 `json:"resume,omitempty"`

	// DrainTimeoutMinutes bounds the wait for all PGs active+clean after
	// the OSD is marked out, and again after the new OSD is in. Past it the
	// replacement pauses; nothing is undone.
	// +optional
	// +kubebuilder:default=120
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=1440
	DrainTimeoutMinutes int32 `json:"drainTimeoutMinutes,omitempty"`
}

// SerialApproval is a serial number retyped by a person.
type SerialApproval struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Serial string `json:"serial"`
}

// OSD replacement phases.
const (
	OSDReplacementChecking              = "Checking"
	OSDReplacementDraining              = "Draining"
	OSDReplacementAwaitingPurgeApproval = "AwaitingPurgeApproval"
	OSDReplacementPurging               = "Purging"
	OSDReplacementAwaitingDisk          = "AwaitingDisk"
	OSDReplacementAwaitingWipeApproval  = "AwaitingWipeApproval"
	OSDReplacementWiping                = "Wiping"
	OSDReplacementDeclaring             = "Declaring"
	OSDReplacementAwaitingOSD           = "AwaitingOSD"
	OSDReplacementBackfilling           = "Backfilling"
	OSDReplacementSucceeded             = "Succeeded"
	OSDReplacementPaused                = "Paused"
	OSDReplacementCancelled             = "Cancelled"
	OSDReplacementFailed                = "Failed"
)

// ReplacedDisk is a disk as the replacement identified it.
type ReplacedDisk struct {
	// +kubebuilder:validation:MaxLength=512
	ByIDPath string `json:"byIdPath,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	Serial string `json:"serial,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	WWN string `json:"wwn,omitempty"`
	// Path is the kernel name when it was read (display only: it changes
	// across boots).
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path,omitempty"`
	// +optional
	SizeGB int32 `json:"sizeGB,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Model string `json:"model,omitempty"`
	// +optional
	LogicalSectorBytes int32 `json:"logicalSectorBytes,omitempty"`
	// +optional
	SMART *DiskSMART `json:"smart,omitempty"`
}

// OSDReplacementEvent is one line of the replacement's own journal.
type OSDReplacementEvent struct {
	At metav1.Time `json:"at"`
	// +kubebuilder:validation:MaxLength=32
	Phase string `json:"phase"`
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message"`
}

type FrameOSDReplacementStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Checking;Draining;AwaitingPurgeApproval;Purging;AwaitingDisk;AwaitingWipeApproval;Wiping;Declaring;AwaitingOSD;Backfilling;Succeeded;Paused;Cancelled;Failed
	Phase string `json:"phase,omitempty"`

	// PausedPhase is the phase a pause interrupted, resumed by spec.resume.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	PausedPhase string `json:"pausedPhase,omitempty"`

	// ObservedResume is the spec.resume last acted on.
	// +optional
	ObservedResume int32 `json:"observedResume,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// +optional
	PhaseSince *metav1.Time `json:"phaseSince,omitempty"`

	// Host is the CRUSH host (and Kubernetes node) of the OSD.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Host string `json:"host,omitempty"`

	// MachineRef is the FrameMachine whose agent reports the host's disks.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	MachineRef string `json:"machineRef,omitempty"`

	// OldDisk is the OSD's disk, identified at the check.
	// +optional
	OldDisk *ReplacedDisk `json:"oldDisk,omitempty"`

	// NewDisk is the disk found after the swap.
	// +optional
	NewDisk *ReplacedDisk `json:"newDisk,omitempty"`

	// DeviceIndex is the old disk's entry in the CephCluster's
	// spec.storage.devices, replaced (and only it) at the declaration.
	// +optional
	DeviceIndex *int32 `json:"deviceIndex,omitempty"`

	// DisksBefore lists the by-id paths of every disk the host had before
	// the purge: the new disk must be absent from it.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=512
	DisksBefore []string `json:"disksBefore,omitempty"`

	// HostOSDsBefore is how many OSDs the host had before: the new OSD is
	// up when it has as many again.
	// +optional
	HostOSDsBefore int32 `json:"hostOSDsBefore,omitempty"`

	// Write-ahead records: each is written before the call it names, so
	// that a restart can tell "never sent" from "sent, outcome unknown".
	// +optional
	MarkedOutAt *metav1.Time `json:"markedOutAt,omitempty"`
	// +optional
	PurgeApprovedAt *metav1.Time `json:"purgeApprovedAt,omitempty"`
	// +optional
	ScaledDownAt *metav1.Time `json:"scaledDownAt,omitempty"`
	// +optional
	PurgeRequestedAt *metav1.Time `json:"purgeRequestedAt,omitempty"`
	// +optional
	PurgedAt *metav1.Time `json:"purgedAt,omitempty"`
	// +optional
	WipeApprovedAt *metav1.Time `json:"wipeApprovedAt,omitempty"`
	// WipeRequestID names the wipe request handed to the node's agent.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	WipeRequestID string `json:"wipeRequestID,omitempty"`
	// +optional
	WipeRequestedAt *metav1.Time `json:"wipeRequestedAt,omitempty"`
	// +optional
	WipedAt *metav1.Time `json:"wipedAt,omitempty"`
	// +optional
	DeclareRequestedAt *metav1.Time `json:"declareRequestedAt,omitempty"`
	// +optional
	DeclaredAt *metav1.Time `json:"declaredAt,omitempty"`
	// +optional
	OperatorRestartedAt *metav1.Time `json:"operatorRestartedAt,omitempty"`
	// +optional
	BackfillSince *metav1.Time `json:"backfillSince,omitempty"`
	// SawUnclean records that, since the last mark, a PG was seen outside
	// active+clean: data did move.
	// +optional
	SawUnclean bool `json:"sawUnclean,omitempty"`

	// PGs is the last count read, "<active+clean>/<total>".
	// +optional
	// +kubebuilder:validation:MaxLength=64
	PGs string `json:"pgs,omitempty"`

	// Events is the replacement's journal, oldest first, the last 64 kept.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Events []OSDReplacementEvent `json:"events,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fosdr
// +kubebuilder:printcolumn:name="OSD",type=integer,JSONPath=".spec.osdID"
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=".status.host"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="PGs",type=string,JSONPath=".status.pgs"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameOSDReplacement is the Schema for the frameosdreplacements API.
type FrameOSDReplacement struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec FrameOSDReplacementSpec `json:"spec,omitempty"`
	// +optional
	Status FrameOSDReplacementStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameOSDReplacementList contains a list of FrameOSDReplacement.
type FrameOSDReplacementList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameOSDReplacement `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameOSDReplacement{}, &FrameOSDReplacementList{})
}

// The disk wipe protocol between the replacement controller and the node's
// agent, on the Node's annotations (the agent already patches its Node).
//
// The controller writes DiskWipeRequestAnnotation, a DiskWipeRequest in
// JSON. The agent wipes only a disk that its own lsblk of the same tick
// reports at that by-id path, with that serial, whole and free (no
// signature, no partition, no mount, no holder), and only once per request
// id (recorded on the host before acting). It answers in
// DiskWipeResultAnnotation, a DiskWipeResult in JSON.
const (
	DiskWipeRequestAnnotation = "frame.plume-labs.io/disk-wipe-request"
	DiskWipeResultAnnotation  = "frame.plume-labs.io/disk-wipe-result"
)

// DiskWipeRequest is the controller's request.
type DiskWipeRequest struct {
	ID          string      `json:"id"`
	ByIDPath    string      `json:"byIdPath"`
	Serial      string      `json:"serial"`
	RequestedAt metav1.Time `json:"requestedAt"`
}

// DiskWipeResult is the agent's answer to the request with the same ID.
type DiskWipeResult struct {
	ID string `json:"id"`
	OK bool   `json:"ok"`
	// Ran says whether any destructive command was started: false means
	// the agent refused before touching the disk.
	Ran     bool        `json:"ran"`
	Message string      `json:"message"`
	At      metav1.Time `json:"at"`
}
