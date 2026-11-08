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

// StorageHealthPolicyName is the only name the kind accepts: the thresholds
// are one cluster-wide setting, so there is nothing to merge or to order.
const StorageHealthPolicyName = "default"

// The thresholds a cluster with no StorageHealthPolicy runs on. The CRD's
// defaults repeat them, so a console that writes one field leaves the other
// where this says.
const (
	DefaultZFSCapacityPercent int32 = 80
	DefaultSSDWearPercent     int32 = 80
)

// StorageHealthPolicySpec is what the machine controller compares a machine's
// storage against. Only numbers: what counts as degraded (a failed md member,
// a pool that is not ONLINE, a vdev with errors) is not a setting.
type StorageHealthPolicySpec struct {
	// ZFSCapacityPercent raises StorageCapacityHigh when a pool's capacity
	// reaches it. ZFS slows down well before a pool is full, and a pool that
	// fills cannot always free space. It clears two points below, so a pool
	// sitting on the threshold does not alert every minute.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	ZFSCapacityPercent int32 `json:"zfsCapacityPercent,omitempty"`

	// SSDWearPercent raises DiskWearHigh when a drive's endurance use, from its
	// BMC or else from the disk's SMART data, reaches it. A drive with neither
	// never raises it: the condition then says Unknown, not False.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	SSDWearPercent int32 `json:"ssdWearPercent,omitempty"`

	// Scrub plans the ZFS scrub of the nodes' pools. Nil means Frame starts
	// none: the distribution's own cron (zfsutils-linux), if any, stays the
	// only one. It is a maintenance setting rather than a threshold, but it
	// lives here because its result is what ScrubErrors judges, and a single
	// cluster-wide object leaves nothing to merge or to order.
	// +optional
	Scrub *ZFSScrubSchedule `json:"scrub,omitempty"`
}

// ZFSScrubSchedule says which nodes are scrubbed, how often, and when a scrub
// may start. One node scrubs at a time, cluster-wide: a node is not asked to
// start while any pool of any node reports a scrub in progress, whoever
// started it. A scrub that runs past its window is left to finish — the window
// bounds when one starts, not how long it lasts.
//
// Disable the distribution's cron on the selected nodes (Ubuntu:
// /etc/cron.d/zfsutils-linux), or two schedules will share the pools: Frame
// still never starts a second scrub beside one in progress, but it cannot
// stop the cron from starting one beside Frame's.
type ZFSScrubSchedule struct {
	// NodeSelector picks the nodes. Nil matches none, like NodeTuning's: a
	// schedule written without one is inert, not fleet-wide by accident. A
	// node is scrubbed only when its FrameMachine reports ZFS present with at
	// least one pool.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// IntervalDays is the time between two completed scrubs of a node.
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=365
	// +optional
	IntervalDays int32 `json:"intervalDays,omitempty"`

	// WindowStart is the time of day ("HH:MM", 24-hour, in TimeZone) from
	// which a scrub may start.
	// +kubebuilder:default="02:00"
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	// +optional
	WindowStart string `json:"windowStart,omitempty"`

	// WindowHours is how long the start window stays open after WindowStart.
	// 24 means any time of day.
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=24
	// +optional
	WindowHours int32 `json:"windowHours,omitempty"`

	// TimeZone is the IANA zone WindowStart is read in ("Europe/Paris").
	// The controller embeds the zone database, so the node's own does not
	// matter.
	// +kubebuilder:default="UTC"
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)*$`
	// +optional
	TimeZone string `json:"timeZone,omitempty"`
}

// The scrub protocol lives on the corev1.Node, like the tuning restart's
// (nodetuning_types.go): per-node facts that survive a controller restart and
// that a human can read and clear with kubectl on the node alone.
//
// The controller writes Requested, Completed, Failed and Unsettled; the agent writes
// Acknowledged and StartedAt. The request carries no pool name: the agent
// scrubs the pools it reads itself (internal/agent/zfsscrub.go).
//
// To stop a request the agent has not taken yet, remove Requested. A scrub
// already running is stopped on the node (`zpool scrub -s <pool>`); the
// controller then records it failed (not completed: the interval does not
// restart on a scrub that never finished). The agent ignores a request older
// than its acknowledgement timeout, so one left behind (policy deleted,
// agent down) does not fire later, outside the window.
const (
	// ZFSScrubRequestedAnnotation is when the controller asked this node to
	// scrub (RFC3339Nano). Its presence is the cluster-wide lock: one node at
	// a time.
	ZFSScrubRequestedAnnotation = "frame.plume-labs.io/zfs-scrub-requested"
	// ZFSScrubAcknowledgedAnnotation is the request value the agent acted on:
	// equal to Requested once every eligible pool was told to scrub.
	ZFSScrubAcknowledgedAnnotation = "frame.plume-labs.io/zfs-scrub-acknowledged"
	// ZFSScrubStartedAtAnnotation is the agent's clock when it started the
	// scrub (RFC3339Nano). The controller waits for a storage read dated after
	// it — the same clock — before trusting that no scrub is in progress.
	ZFSScrubStartedAtAnnotation = "frame.plume-labs.io/zfs-scrub-started-at"
	// ZFSScrubCompletedAnnotation is when the controller last saw this
	// node's requested scrub finish (RFC3339Nano). IntervalDays counts from it.
	ZFSScrubCompletedAnnotation = "frame.plume-labs.io/zfs-scrub-completed"
	// ZFSScrubFailedAnnotation is when a request was given up (RFC3339Nano):
	// the agent never took it, its node stopped reporting after the start,
	// or the scrub ended without completing. The node is not asked again
	// for a day.
	ZFSScrubFailedAnnotation = "frame.plume-labs.io/zfs-scrub-failed"
	// ZFSScrubUnsettledAnnotation keeps the cluster-wide lock after a
	// failure, since a scrub can be running all the same. Its value is the
	// node's last storage read date at the failure ("none" without one); the
	// controller removes it once a complete read dated after it shows no
	// scan. Removing it by hand releases the lock.
	ZFSScrubUnsettledAnnotation = "frame.plume-labs.io/zfs-scrub-unsettled"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the policy is a singleton named default"
// +kubebuilder:printcolumn:name="ZFS capacity %",type=integer,JSONPath=`.spec.zfsCapacityPercent`
// +kubebuilder:printcolumn:name="SSD wear %",type=integer,JSONPath=`.spec.ssdWearPercent`

// StorageHealthPolicy holds the thresholds behind FrameMachine's storage
// conditions and alerts. Its absence means the defaults above. It has no
// status: nothing reconciles it, the machine controller reads it on each pass.
type StorageHealthPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec StorageHealthPolicySpec `json:"spec"`
}

// +kubebuilder:object:root=true

// StorageHealthPolicyList contains a list of StorageHealthPolicy.
type StorageHealthPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StorageHealthPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StorageHealthPolicy{}, &StorageHealthPolicyList{})
}
