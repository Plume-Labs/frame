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
}
