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

// PowerAction is a request to change the machine's power state, clear its
// event log, or set its identification LED.
// +kubebuilder:validation:Enum=On;GracefulShutdown;ForceOff;ForceRestart;ClearSEL;IndicatorLedOn;IndicatorLedOff
type PowerAction string

const (
	PowerActionOn               PowerAction = "On"
	PowerActionGracefulShutdown PowerAction = "GracefulShutdown"
	PowerActionForceOff         PowerAction = "ForceOff"
	PowerActionForceRestart     PowerAction = "ForceRestart"
	PowerActionClearSEL         PowerAction = "ClearSEL"
	PowerActionIndicatorLedOn   PowerAction = "IndicatorLedOn"
	PowerActionIndicatorLedOff  PowerAction = "IndicatorLedOff"
)

// BMCTLSSpec says how the controller verifies the BMC's certificate. There is
// deliberately no permissive default: an iLO4 ships a self-signed certificate,
// so a machine registered without a choice here sits Reachable=false with a
// TLS error until someone makes one. The bypass is a value in the spec,
// visible in `kubectl get -o yaml` and in review, rather than a default buried
// in the client.
type BMCTLSSpec struct {
	// InsecureSkipVerify disables certificate verification entirely.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// CABundleRef names a ConfigMap in the same namespace holding the CA that
	// signed the BMC's certificate under the key `ca.crt`.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	CABundleRef string `json:"caBundleRef,omitempty"`
}

// BMCSpec locates the machine's baseboard management controller.
//
// +kubebuilder:validation:XValidation:rule="self.tls.insecureSkipVerify == true || has(self.tls.caBundleRef)",message="bmc.tls must set either insecureSkipVerify or caBundleRef"
type BMCSpec struct {
	// Address is the IP of the management port.
	//
	// It is an IP literal and not a hostname on purpose. The controller
	// connects to this address carrying the credentials named below, which
	// makes this field a server-side request forgery primitive; requiring a
	// literal takes name resolution out of that path. Loopback is the
	// operator's own pod and link-local is where cloud metadata services
	// live, so both are refused.
	//
	// MaxLength is what lets the CEL cost estimator bound isIP(), the same
	// reason FrameNodeSpec.IP carries one. 45 matches that field.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="bmc.address must be a valid IP address"
	// +kubebuilder:validation:XValidation:rule="!cidr('127.0.0.0/8').containsIP(self)",message="bmc.address must not be a loopback address"
	// +kubebuilder:validation:XValidation:rule="!cidr('169.254.0.0/16').containsIP(self)",message="bmc.address must not be a link-local address"
	Address string `json:"address"`

	// CredentialsRef names a Secret in the same namespace holding the keys
	// `username` and `password`. Only the controller reads it; no console
	// tier is granted `secrets`.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	CredentialsRef string `json:"credentialsRef"`

	// TLS is required, and one of its two fields must be set.
	// +kubebuilder:validation:Required
	TLS BMCTLSSpec `json:"tls"`
}

// PowerRequestSpec is an action, not a desired state.
//
// A controller holding "desired: On" would turn a machine back on after
// someone pressed its physical power button — it would fight the person
// standing at the rack — and could not express a restart at all, since the
// state before and after is identical. The controller acts only when
// RequestedAt is later than status.lastPowerActionAt. The same pattern is
// already in this codebase: lot 2 restarts a Deployment by writing
// restartedAt.
type PowerRequestSpec struct {
	// +kubebuilder:validation:Required
	Action PowerAction `json:"action"`

	// +kubebuilder:validation:Required
	RequestedAt metav1.Time `json:"requestedAt"`
}

// FrameMachineSpec defines the desired state of FrameMachine.
type FrameMachineSpec struct {
	// +kubebuilder:validation:Required
	BMC BMCSpec `json:"bmc"`

	// +optional
	PowerRequest *PowerRequestSpec `json:"powerRequest,omitempty"`

	// NodeRef names the Kubernetes node this chassis carries, when it carries
	// one. It names a Kubernetes node and not a FrameNode deliberately:
	// FrameNode's provisioning flow is Talos-shaped and the estate moved to a
	// Debian-based image (see docs/provisioning.md), whereas a Kubernetes node
	// name is true regardless of how the machine was installed.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	NodeRef string `json:"nodeRef,omitempty"`
}

// Inventory string fields below are free text the BMC reports, not
// something this package controls the length of, so each carries a MaxLength
// bound (256 unless the field is known to be short, e.g. a MAC address) —
// without one, etcd's object size limit is the only backstop, and hitting it
// rejects the whole status write rather than just truncating one field.

// ProcessorInfo describes one installed CPU.
type ProcessorInfo struct {
	// +kubebuilder:validation:MaxLength=64
	Socket string `json:"socket,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Model   string `json:"model,omitempty"`
	Cores   int32  `json:"cores,omitempty"`
	Threads int32  `json:"threads,omitempty"`
}

// MemoryModuleInfo describes one installed DIMM and where it sits.
type MemoryModuleInfo struct {
	// +kubebuilder:validation:MaxLength=64
	Slot    string `json:"slot,omitempty"`
	SizeMiB int32  `json:"sizeMiB,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	Type string `json:"type,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Manufacturer string `json:"manufacturer,omitempty"`
}

// DriveInfo describes one drive the BMC can see. It is half of the storage
// picture: what the node's own kernel sees lives in
// FrameMachineStatus.Storage.Observed, and the two are joined on
// SerialNumber alone (internal/storage.Join).
type DriveInfo struct {
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Model  string `json:"model,omitempty"`
	SizeGB int32  `json:"sizeGB,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	Protocol string `json:"protocol,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	Health string `json:"health,omitempty"`

	// SerialNumber is the only key this drive can be matched on. A Redfish
	// drive name and a /dev/disk/by-id path are different namespaces.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	SerialNumber string `json:"serialNumber,omitempty"`

	// Location is the physical bay, in the controller's
	// ControllerPort:Box:Bay format (e.g. "2I:6:8") — the string a human
	// reads on the chassis before pulling a disk.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Location string `json:"location,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=32
	MediaType string `json:"mediaType,omitempty"`

	// StatusReasons is what the controller says about this drive's state.
	// On the captured ML350 G9 all eight drives report ["None"], including
	// the one the host cannot see: the BMC has no field that admits a disk
	// is masked. That is why divergence is computed from two sources.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	StatusReasons []string `json:"statusReasons,omitempty"`
}

// ObservedDisk is one whole disk as the node's own kernel reports it —
// the second of the two storage sources. It is never merged with
// DriveInfo: the gap between them is the datum (see
// docs/superpowers/specs/2026-09-13-storage-design.md §3).
type ObservedDisk struct {
	// Path is the stable /dev/disk/by-id path where one exists, and the
	// kernel name otherwise. It is never an sdX name in a destructive
	// context: sdX ordering changed on all three boots of the machine this
	// design was written against.
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path,omitempty"`

	// SerialNumber is the join key. An entry never carries an empty one:
	// the agent drops a disk it could not identify rather than publish a
	// blank that matches every other blank.
	// +kubebuilder:validation:MaxLength=128
	SerialNumber string `json:"serialNumber,omitempty"`

	SizeGB int32 `json:"sizeGB,omitempty"`

	// Occupancy is what is using the disk: free, mounted, partitioned,
	// lvm-pv, ceph-osd, or in-use for a signature the agent does not
	// recognise. Anything but "free" makes FrameDiskClaim refuse.
	// +kubebuilder:validation:Enum=free;mounted;partitioned;lvm-pv;ceph-osd;in-use
	// +kubebuilder:validation:MaxLength=32
	Occupancy string `json:"occupancy,omitempty"`
}

// DiskDivergence is one disagreement between the BMC's list and the node's.
// It is computed by internal/storage.Join and never read from either source:
// on the captured ML350 G9 the BMC reports Health OK and
// DiskDriveStatusReasons ["None"] for the very disk the kernel cannot see.
type DiskDivergence struct {
	// +kubebuilder:validation:MaxLength=128
	SerialNumber string `json:"serialNumber,omitempty"`

	// Reason is bmc-only (the BMC lists it, the kernel does not),
	// os-only (the reverse), or mismatch (both list it, disagreeing).
	// +kubebuilder:validation:Enum=bmc-only;os-only;mismatch
	Reason string `json:"reason,omitempty"`

	// Detail names what differs, for mismatch. A divergence with no detail
	// says two numbers differ without saying which.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Detail string `json:"detail,omitempty"`
}

// MachineStorage is the node-side half of the storage picture plus the
// computed gap. The BMC-side half stays where the previous lot put it,
// in Inventory.Drives.
type MachineStorage struct {
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Observed []ObservedDisk `json:"observed,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=64
	Divergences []DiskDivergence `json:"divergences,omitempty"`

	// ObservedAt is when the agent last reported. A nil value means never,
	// and never is not the same as "no disks" — FrameDiskClaim refuses on
	// both, deliberately, but the screens must be able to tell them apart.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}

// NetworkAdapterInfo describes one network port.
type NetworkAdapterInfo struct {
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	MAC string `json:"mac,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	Status string `json:"status,omitempty"`
}

// MachineInventory is what the machine is made of.
type MachineInventory struct {
	// +kubebuilder:validation:MaxLength=256
	Manufacturer string `json:"manufacturer,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Model string `json:"model,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	SerialNumber string `json:"serialNumber,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	BIOSVersion string `json:"biosVersion,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	BMCFirmware string `json:"bmcFirmware,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	Processors []ProcessorInfo `json:"processors,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=48
	MemoryModules []MemoryModuleInfo `json:"memoryModules,omitempty"`

	TotalMemoryGiB int32 `json:"totalMemoryGiB,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=32
	Drives []DriveInfo `json:"drives,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=16
	NetworkAdapters []NetworkAdapterInfo `json:"networkAdapters,omitempty"`
}

// TemperatureReading is one temperature sensor.
type TemperatureReading struct {
	Name string `json:"name,omitempty"`

	// Celsius carries no `omitempty`: a stopped-but-present sensor can
	// genuinely read 0, and dropping the field on that value would make it
	// indistinguishable from a sensor never populated at all — the exact
	// inversion of what this lot exists to prevent (see internal/redfish's
	// absent-sensor handling for the socket-vs-reading distinction this
	// guards separately).
	Celsius       int32  `json:"celsius"`
	UpperCritical *int32 `json:"upperCritical,omitempty"`
	Health        string `json:"health,omitempty"`
}

// FanReading is one fan. Units is what the BMC reported the reading in —
// iLO4 commonly says Percent where other vendors say RPM, so the number is
// meaningless without it.
type FanReading struct {
	Name string `json:"name,omitempty"`

	// Reading carries no `omitempty`, for the same reason
	// TemperatureReading.Celsius doesn't: a stopped fan reading 0 must stay
	// distinguishable from a fan bay this decoder never populated.
	Reading int32  `json:"reading"`
	Units   string `json:"units,omitempty"`
	Health  string `json:"health,omitempty"`
}

// PowerSupplyReading is one PSU.
type PowerSupplyReading struct {
	Name                 string `json:"name,omitempty"`
	Health               string `json:"health,omitempty"`
	State                string `json:"state,omitempty"`
	LastPowerOutputWatts int32  `json:"lastPowerOutputWatts,omitempty"`
}

// MachineSensors is what the machine currently reads.
type MachineSensors struct {
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Temperatures []TemperatureReading `json:"temperatures,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=32
	Fans []FanReading `json:"fans,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	PowerSupplies []PowerSupplyReading `json:"powerSupplies,omitempty"`

	// PowerConsumedWatts carries no `omitempty`: 0 W is the machine's own
	// reading while powered off or mid-POST (see PROVENANCE.md), and must
	// stay distinguishable from a reading this decoder never took.
	PowerConsumedWatts int32 `json:"powerConsumedWatts"`
}

// EventLogEntry is one line of the machine's event log.
type EventLogEntry struct {
	// +kubebuilder:validation:MaxLength=64
	ID string `json:"id,omitempty"`
	// +kubebuilder:validation:MaxLength=32
	Severity string `json:"severity,omitempty"`
	// Message is free text from the BMC's own log and this package does not
	// control its length. MaxLength bounds it so twenty-five unbounded
	// messages cannot grow the status past etcd's object size limit and
	// reject the whole status write.
	// +kubebuilder:validation:MaxLength=512
	Message string      `json:"message,omitempty"`
	Created metav1.Time `json:"created,omitempty"`
}

// FrameMachineStatus defines the observed state of FrameMachine.
//
// No status.phase: the Ready-equivalent condition is Reachable, and its reason
// carries why — Probed when it succeeded, and one of TLSError, AuthFailed,
// Timeout, Unsupported, CredentialsUnavailable or ProbeFailed when it did not.
type FrameMachineStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	PowerState string `json:"powerState,omitempty"`

	// PostState is the machine's power-on-self-test state as the BMC
	// reports it.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	PostState string `json:"postState,omitempty"`

	// +optional
	IndicatorLED string `json:"indicatorLED,omitempty"`

	// +optional
	Inventory *MachineInventory `json:"inventory,omitempty"`

	// Storage carries what the node itself reports about its disks, and the
	// divergences against Inventory.Drives. Two writers touch this object's
	// status — the machine controller and the node agent — so the agent
	// writes nothing but this field (see internal/agent/diskstatus.go).
	// +optional
	Storage *MachineStorage `json:"storage,omitempty"`

	// Sensors is nil whenever the last probe found the readings untrustworthy
	// (see SensorsValidAt) rather than kept alongside a caveat nobody reads —
	// see the controller's applySnapshot for why clearing, not flagging, is
	// the deliberate choice.
	// +optional
	Sensors *MachineSensors `json:"sensors,omitempty"`

	// SensorsValidAt is when Sensors was last read in a state where the
	// machine could actually produce it. It is deliberately distinct from
	// LastProbeAt: a probe can succeed against a powered-off machine and
	// come back with a full set of readings that describe an earlier moment
	// (see internal/redfish.Snapshot.SensorsTrustworthy) — LastProbeAt still
	// advances on that probe, but SensorsValidAt does not.
	// +optional
	SensorsValidAt *metav1.Time `json:"sensorsValidAt,omitempty"`

	// EventLog holds the most recent entries only. etcd is not a log store: a
	// machine up for years can hold thousands, and twenty-five is enough to
	// answer "why did it reboot", which is the question the log is for.
	// +optional
	// +kubebuilder:validation:MaxItems=25
	EventLog []EventLogEntry `json:"eventLog,omitempty"`

	// EventLogCounts is the count per severity across the entries this probe
	// actually retrieved from the BMC — one page, not necessarily the whole
	// log. The redfish client pages to the machine's last page when the log
	// spans more than one (internal/redfish's readLog/fetchLastLogPage), so
	// this is normally the newest page, but the BMC's own page size is not
	// fixed and the last page is typically shorter than the others: on the
	// captured iLO4 the machine's own log holds 175 entries (EventLogTotal)
	// across pages of 30, except a final page of 25 — so EventLogCounts here
	// sums to 25, the size of the page actually retrieved, not to
	// EventLogTotal. EventLogTotal is the number to read for "how many
	// entries does the machine say it holds"; this field never claims to
	// cover more than what it counted. See EventLogPossiblyStale for whether
	// that retrieved page is even confirmed to be the newest one.
	// +optional
	EventLogCounts map[string]int32 `json:"eventLogCounts,omitempty"`

	// +optional
	EventLogTotal int32 `json:"eventLogTotal,omitempty"`

	// EventLogPossiblyStale is true when the BMC's IML log spans more than
	// one page and the redfish client could not confirm it reached the true
	// last page (internal/redfish.Snapshot.LogPossiblyStale) — a BMC whose
	// out-of-range-page error doesn't match the one this client knows how to
	// read, for instance. When true, EventLog/EventLogCounts above still
	// populate, from whatever page the client did read, but that page may be
	// the machine's *oldest* entries rather than its newest: nothing else in
	// this status distinguishes the two cases. Absent (false) both when the
	// log fits on one page and when the jump to the last page succeeded.
	// +optional
	EventLogPossiblyStale bool `json:"eventLogPossiblyStale,omitempty"`

	// LastProbeAt is when the values above were read. Every panel renders it:
	// a sensor value with no timestamp is a lie the moment the BMC stops
	// answering.
	// +optional
	LastProbeAt *metav1.Time `json:"lastProbeAt,omitempty"`

	// +optional
	LastPowerAction string `json:"lastPowerAction,omitempty"`

	// LastPowerActionAt is the guard: a powerRequest is executed only when its
	// requestedAt is strictly later than this.
	// +optional
	LastPowerActionAt *metav1.Time `json:"lastPowerActionAt,omitempty"`

	// LastPowerActionError carries why the last power action failed, and is
	// cleared when one succeeds. Without it a failed action is
	// indistinguishable from a successful one: lastPowerActionAt advances
	// either way — deliberately, so a failing action is not retried every
	// sixty seconds forever — and the Event that carries the failure ages
	// out while the timestamp does not.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	LastPowerActionError string `json:"lastPowerActionError,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fm
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=".spec.bmc.address"
// +kubebuilder:printcolumn:name="Power",type=string,JSONPath=".status.powerState"
// +kubebuilder:printcolumn:name="Reachable",type=string,JSONPath=`.status.conditions[?(@.type=="Reachable")].status`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=".status.inventory.model"
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=".spec.nodeRef"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameMachine is the Schema for the framemachines API.
type FrameMachine struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec FrameMachineSpec `json:"spec,omitempty"`

	// +optional
	Status FrameMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameMachineList contains a list of FrameMachine.
type FrameMachineList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard list metadata
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []FrameMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameMachine{}, &FrameMachineList{})
}
