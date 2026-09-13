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

package redfish

import (
	"errors"
	"time"
)

// These errors are what the controller turns into a Reachable condition
// reason, so they are part of this package's contract, not an implementation
// detail.
var (
	ErrAuth        = errors.New("redfish: authentication rejected")
	ErrTLS         = errors.New("redfish: TLS verification failed")
	ErrUnsupported = errors.New("redfish: service does not expose the expected resources")
)

// EventLogRetainCount is how many of the newest event log entries this
// package, the controller, and the CRD schema all agree to keep — one
// source of truth for a number that used to exist independently in three
// places (this package's readLog, the controller's mapEventLog, and
// FrameMachineStatus.EventLog's kubebuilder MaxItems, which cannot reference
// a Go constant and must keep the literal 25 in sync with this by hand).
// etcd is not a log store: a machine up for years can hold thousands of
// entries, and twenty-five recent ones are enough to answer "why did it
// reboot".
const EventLogRetainCount = 25

type Processor struct {
	Socket  string
	Model   string
	Cores   int32
	Threads int32
}

type MemoryModule struct {
	Slot         string
	SizeMiB      int32
	Type         string
	Manufacturer string
}

type Drive struct {
	Name     string
	Model    string
	SizeGB   int32
	Protocol string
	Health   string

	// SerialNumber is the join key against what the node's own agent sees
	// (internal/storage.Join). A Redfish drive name and a /dev/disk/by-id
	// path are different namespaces; matching on anything but the serial
	// matches nothing.
	SerialNumber string

	// Location is the physical bay in the controller's own
	// ControllerPort:Box:Bay format, e.g. "2I:6:8" — what a human reads on
	// the chassis before pulling a disk.
	Location string

	// MediaType is HDD or SSD as the controller reports it.
	MediaType string

	// StatusReasons is the controller's own explanation of the drive's
	// state. On the captured hardware every drive, including the one Linux
	// cannot see, reports ["None"]: the BMC has no field that says a disk
	// is masked by residual logical-unit metadata. That absence is why the
	// divergence is computed, never read.
	StatusReasons []string
}

type NetworkAdapter struct {
	Name   string
	MAC    string
	Status string
}

type Inventory struct {
	Manufacturer    string
	Model           string
	SerialNumber    string
	BIOSVersion     string
	BMCFirmware     string
	Processors      []Processor
	MemoryModules   []MemoryModule
	TotalMemoryGiB  int32
	Drives          []Drive
	NetworkAdapters []NetworkAdapter
}

type Temperature struct {
	Name          string
	Celsius       int32
	UpperCritical *int32
	Health        string
}

type Fan struct {
	Name    string
	Reading int32
	Units   string
	Health  string
}

type PowerSupply struct {
	Name                 string
	Health               string
	State                string
	LastPowerOutputWatts int32
}

type Sensors struct {
	Temperatures       []Temperature
	Fans               []Fan
	PowerSupplies      []PowerSupply
	PowerConsumedWatts int32
}

type LogEntry struct {
	ID       string
	Severity string
	Message  string
	Created  time.Time
}

// Snapshot is one complete read of a machine. Probe returns it whole so a
// reconcile is one call rather than seven, and so a partial failure is a
// failure of the snapshot rather than a half-written status.
type Snapshot struct {
	PowerState   string
	IndicatorLED string
	Inventory    Inventory
	Sensors      Sensors
	Log          []LogEntry
	LogTotal     int
	LogCounts    map[string]int

	// PostState is the machine's power-on-self-test state, read from the HPE
	// OEM block. iLO4 names that block `Hp`; `Hpe` is iLO5 and does not appear
	// here.
	PostState string

	// SensorsTrustworthy says whether Sensors describes the machine now or an
	// earlier moment the BMC is still replaying. It is false whenever the
	// machine is not powered on, and while it is still in POST.
	//
	// This exists because the readings themselves cannot be told apart. On
	// the captured hardware CPU1 reports 40 C with Status.State "Enabled"
	// whether the machine is off, mid-POST, or finished — twenty minutes
	// after being powered down, in a room at 20 C. Neither the value, nor
	// the health, nor the sensor count distinguishes a measurement from a
	// memory. See internal/redfish/testdata/ilo4-real/PROVENANCE.md.
	SensorsTrustworthy bool

	// AllowableResetTypes is what the machine's
	// Actions.#ComputerSystem.Reset["ResetType@Redfish.AllowableValues"]
	// advertises. It exists so Reset can resolve a caller's request (in
	// particular GracefulShutdown, which the captured iLO4 does not list at
	// all) against what this specific machine actually accepts, rather than
	// sending a string the BMC will reject.
	AllowableResetTypes []string

	// LogPossiblyStale is true when the IML log spans more than one page
	// (the first page's own links.NextPage said so) and readLog could not
	// confirm it reached the true last page — decode_log.go's
	// fetchLastLogPage failed for any reason: an out-of-range probe that
	// didn't come back as Base.0.10.QueryParameterOutOfRange, a body that
	// didn't parse, a transport error, or anything else. When that happens,
	// readLog keeps the first page rather than guessing further, and the
	// first page is this machine's *oldest* entries, not its newest — the
	// exact failure mode C1 exists to prevent, recurring behind a firmware
	// that doesn't answer the jump the way PROVENANCE.md's capture does.
	// Without this flag that recurrence is invisible: Log and LogCounts
	// still populate, LogTotal is still correct, and nothing else in the
	// snapshot distinguishes "these are the newest 25" from "these are the
	// oldest 25, silently". False for a log that fits on one page (there is
	// nothing to jump to) and for a jump that succeeded.
	LogPossiblyStale bool
}
