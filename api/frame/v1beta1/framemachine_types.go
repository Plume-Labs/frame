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

// ProcessorInfo describes one installed CPU.
type ProcessorInfo struct {
	Socket  string `json:"socket,omitempty"`
	Model   string `json:"model,omitempty"`
	Cores   int32  `json:"cores,omitempty"`
	Threads int32  `json:"threads,omitempty"`
}

// MemoryModuleInfo describes one installed DIMM and where it sits.
type MemoryModuleInfo struct {
	Slot         string `json:"slot,omitempty"`
	SizeMiB      int32  `json:"sizeMiB,omitempty"`
	Type         string `json:"type,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
}

// DriveInfo describes one drive the BMC can see.
type DriveInfo struct {
	Name     string `json:"name,omitempty"`
	Model    string `json:"model,omitempty"`
	SizeGB   int32  `json:"sizeGB,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Health   string `json:"health,omitempty"`
}

// NetworkAdapterInfo describes one network port.
type NetworkAdapterInfo struct {
	Name   string `json:"name,omitempty"`
	MAC    string `json:"mac,omitempty"`
	Status string `json:"status,omitempty"`
}

// MachineInventory is what the machine is made of.
type MachineInventory struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serialNumber,omitempty"`
	BIOSVersion  string `json:"biosVersion,omitempty"`
	BMCFirmware  string `json:"bmcFirmware,omitempty"`

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
	Name          string `json:"name,omitempty"`
	Celsius       int32  `json:"celsius,omitempty"`
	UpperCritical *int32 `json:"upperCritical,omitempty"`
	Health        string `json:"health,omitempty"`
}

// FanReading is one fan. Units is what the BMC reported the reading in —
// iLO4 commonly says Percent where other vendors say RPM, so the number is
// meaningless without it.
type FanReading struct {
	Name    string `json:"name,omitempty"`
	Reading int32  `json:"reading,omitempty"`
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

	PowerConsumedWatts int32 `json:"powerConsumedWatts,omitempty"`
}

// EventLogEntry is one line of the machine's event log.
type EventLogEntry struct {
	ID       string      `json:"id,omitempty"`
	Severity string      `json:"severity,omitempty"`
	Message  string      `json:"message,omitempty"`
	Created  metav1.Time `json:"created,omitempty"`
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

	// EventLogCounts is the count per severity across the whole log, not just
	// the retained entries.
	// +optional
	EventLogCounts map[string]int32 `json:"eventLogCounts,omitempty"`

	// +optional
	EventLogTotal int32 `json:"eventLogTotal,omitempty"`

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
