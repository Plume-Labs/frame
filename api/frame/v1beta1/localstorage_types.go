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

// The node's local storage stacks, as the node agent reads them
// (internal/agent/localstorage.go). Every list and string is bounded: the
// agent clips and truncates to these markers, because one value past them
// gets the machine's whole status.storage write rejected. Where a list is
// cut, an *Omitted counter says how many entries were left out — a short
// list must never read as a complete one.

// StackState says whether a storage stack runs on the node.
//
// absent: the node does not run it (no zfs module, no LVM tools) — normal,
// not an error. present: it was read. error: it runs but could not be read;
// Error says why, and the lists are empty rather than stale.
// +kubebuilder:validation:Enum=present;absent;error
type StackState string

const (
	StackPresent StackState = "present"
	StackAbsent  StackState = "absent"
	StackError   StackState = "error"
)

// LocalStorage groups the three stacks.
type LocalStorage struct {
	ZFS ZFSStatus `json:"zfs"`
	MD  MDStatus  `json:"md"`
	LVM LVMStatus `json:"lvm"`
}

// ZFSStatus is `zpool list`, `zpool status -P` and `zfs list`.
type ZFSStatus struct {
	State StackState `json:"state"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Error string `json:"error,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Pools []ZPool `json:"pools,omitempty"`
	// +optional
	PoolsOmitted int32 `json:"poolsOmitted,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Datasets []ZFSDataset `json:"datasets,omitempty"`
	// +optional
	DatasetsOmitted int32 `json:"datasetsOmitted,omitempty"`
}

// ZPool is one pool.
type ZPool struct {
	// +kubebuilder:validation:MaxLength=128
	Name string `json:"name"`
	// Health as zpool reports it: ONLINE, DEGRADED, FAULTED, OFFLINE,
	// UNAVAIL, REMOVED, SUSPENDED.
	// +kubebuilder:validation:MaxLength=32
	Health         string `json:"health,omitempty"`
	SizeBytes      int64  `json:"sizeBytes,omitempty"`
	AllocatedBytes int64  `json:"allocatedBytes,omitempty"`
	FreeBytes      int64  `json:"freeBytes,omitempty"`
	// +optional
	FragmentationPercent *int32 `json:"fragmentationPercent,omitempty"`
	// +optional
	CapacityPercent *int32 `json:"capacityPercent,omitempty"`
	// Scan is zpool status's scan line, verbatim (clipped).
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Scan string `json:"scan,omitempty"`
	// ScrubErrors is the error count of the last completed scrub; nil when
	// no scrub has completed (none requested, or one in progress).
	// +optional
	ScrubErrors *int32 `json:"scrubErrors,omitempty"`
	// LastScrub is the completion date as zpool prints it.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	LastScrub string `json:"lastScrub,omitempty"`
	// Errors is zpool status's errors line ("No known data errors").
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Errors string `json:"errors,omitempty"`
	// Vdevs is the config tree flattened in display order; Depth is the
	// nesting under the pool (0 = top-level vdev or a logs/cache/spares
	// group). A CRD schema cannot recurse, hence the flat list.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Vdevs []ZVdev `json:"vdevs,omitempty"`
	// +optional
	VdevsOmitted int32 `json:"vdevsOmitted,omitempty"`
}

// ZVdev is one row of a pool's config tree.
type ZVdev struct {
	// Name is the vdev name or, for a leaf, its device path (zpool status
	// -P): the by-id path is what the console joins to a disk's serial.
	// +kubebuilder:validation:MaxLength=512
	Name  string `json:"name"`
	Depth int32  `json:"depth"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	State string `json:"state,omitempty"`
	// +optional
	ReadErrors int64 `json:"readErrors,omitempty"`
	// +optional
	WriteErrors int64 `json:"writeErrors,omitempty"`
	// +optional
	ChecksumErrors int64 `json:"checksumErrors,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Note string `json:"note,omitempty"`
}

// ZFSDataset is one filesystem or volume (`zfs list`, snapshots excluded).
type ZFSDataset struct {
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=16
	Type       string `json:"type,omitempty"`
	UsedBytes  int64  `json:"usedBytes,omitempty"`
	AvailBytes int64  `json:"availBytes,omitempty"`
	ReferBytes int64  `json:"referBytes,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=512
	MountPoint string `json:"mountPoint,omitempty"`
}

// MDStatus is /proc/mdstat.
type MDStatus struct {
	State StackState `json:"state"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Error string `json:"error,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Arrays []MDArray `json:"arrays,omitempty"`
	// +optional
	ArraysOmitted int32 `json:"arraysOmitted,omitempty"`
}

// MDArray is one md device.
type MDArray struct {
	// +kubebuilder:validation:MaxLength=32
	Name string `json:"name"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Level string `json:"level,omitempty"`
	// State is active or inactive (and "active (auto-read-only)"…), verbatim.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	State string `json:"state,omitempty"`
	// RaidDisks and ActiveDisks are the [n/m] of mdstat; zero when mdstat
	// prints none (raid0, linear, an inactive array).
	// +optional
	RaidDisks int32 `json:"raidDisks,omitempty"`
	// +optional
	ActiveDisks int32 `json:"activeDisks,omitempty"`
	// Slots is mdstat's [UU_] string: one U per working member.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Slots string `json:"slots,omitempty"`
	// Degraded is computed by the agent: fewer active than raid disks, a
	// '_' slot, a failed member, or an inactive array.
	Degraded bool `json:"degraded"`
	// Sync is the resync/recovery/check/reshape progress line, e.g.
	// "recovery 8.5%", or "resync DELAYED".
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Sync string `json:"sync,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Members []MDMember `json:"members,omitempty"`
}

// MDMember is one component device.
type MDMember struct {
	// Device is the kernel name as mdstat prints it (sdc2).
	// +kubebuilder:validation:MaxLength=64
	Device string `json:"device"`
	// Serial is the serial of the whole disk holding Device, resolved from
	// the same agent cycle's lsblk; empty when it could not be.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Serial string `json:"serial,omitempty"`
	// +optional
	Failed bool `json:"failed,omitempty"`
	// +optional
	Spare bool `json:"spare,omitempty"`
}

// LVMStatus is `pvs`, `vgs` and `lvs` in JSON.
type LVMStatus struct {
	State StackState `json:"state"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Error string `json:"error,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=64
	PVs []LVMPhysicalVolume `json:"pvs,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=32
	VGs []LVMVolumeGroup `json:"vgs,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=128
	LVs []LVMLogicalVolume `json:"lvs,omitempty"`
	// +optional
	Omitted int32 `json:"omitted,omitempty"`
}

// LVMPhysicalVolume is one PV.
type LVMPhysicalVolume struct {
	// +kubebuilder:validation:MaxLength=512
	Name string `json:"name"`
	// +optional
	// +kubebuilder:validation:MaxLength=128
	VG        string `json:"vg,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	FreeBytes int64  `json:"freeBytes,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Attr string `json:"attr,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Serial string `json:"serial,omitempty"`
}

// LVMVolumeGroup is one VG. Attr's fourth character 'p' means partial —
// a PV is missing.
type LVMVolumeGroup struct {
	// +kubebuilder:validation:MaxLength=128
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	FreeBytes int64  `json:"freeBytes,omitempty"`
	PVCount   int32  `json:"pvCount,omitempty"`
	LVCount   int32  `json:"lvCount,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Attr string `json:"attr,omitempty"`
}

// LVMLogicalVolume is one LV.
type LVMLogicalVolume struct {
	// +kubebuilder:validation:MaxLength=128
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=128
	VG        string `json:"vg"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Attr string `json:"attr,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	SegType string `json:"segType,omitempty"`
	// DataPercent is lvs's data_percent for thin pools and snapshots,
	// verbatim ("" otherwise).
	// +optional
	// +kubebuilder:validation:MaxLength=16
	DataPercent string `json:"dataPercent,omitempty"`
}
