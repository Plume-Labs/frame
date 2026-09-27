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
	"context"
	"crypto/tls"
	"encoding/json"
	"testing"
)

// probeRealSmartStorage runs a full Probe against the captured ilo4-real
// SmartStorage tree. Drives are looked up by bay (Location), never by
// serial, so no serial number is repeated in this file.
func probeRealSmartStorage(t *testing.T) *Snapshot {
	t.Helper()
	srv := serveRealSmartStorage(t)
	c := New(srv.URL, "u", "p", &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test server
	snap, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return snap
}

func driveAt(t *testing.T, snap *Snapshot, bay string) Drive {
	t.Helper()
	for _, d := range snap.Inventory.Drives {
		if d.Location == bay {
			return d
		}
	}
	t.Fatalf("no drive in bay %s among %d drives", bay, len(snap.Inventory.Drives))
	return Drive{}
}

// TestRealDrivesReportFirmwareSpeedAndLink pins the three plain fields on
// the real capture, one bay per drive family.
func TestRealDrivesReportFirmwareSpeedAndLink(t *testing.T) {
	snap := probeRealSmartStorage(t)
	tests := []struct {
		bay      string
		firmware string
		rpm      int32
		linkMbps int32
	}{
		{"1I:6:4", "C6B0", 10000, 6000},
		{"1I:6:1", "HPD6", 10500, 12000},
		{"2I:6:8", "HPG5", 7200, 6000},
	}
	for _, tt := range tests {
		t.Run(tt.bay, func(t *testing.T) {
			d := driveAt(t, snap, tt.bay)
			if d.Firmware != tt.firmware {
				t.Errorf("Firmware = %q, want %q", d.Firmware, tt.firmware)
			}
			if d.RotationSpeedRPM != tt.rpm {
				t.Errorf("RotationSpeedRPM = %d, want %d", d.RotationSpeedRPM, tt.rpm)
			}
			if d.LinkSpeedMbps != tt.linkMbps {
				t.Errorf("LinkSpeedMbps = %d, want %d", d.LinkSpeedMbps, tt.linkMbps)
			}
		})
	}
}

// TestRealZeroTemperatureIsNotPublished is the guard this lot exists for:
// the captured iLO4 reports CurrentTemperatureCelsius 0 for all eight
// drives — including bays whose MaximumTemperatureCelsius is 62 — and null
// PowerOnHours and SSD endurance. None of that may surface as a reading. A
// disk shown at 0 °C is a disk shown as fine when nobody measured it.
func TestRealZeroTemperatureIsNotPublished(t *testing.T) {
	snap := probeRealSmartStorage(t)
	if len(snap.Inventory.Drives) != 8 {
		t.Fatalf("Drives = %d, want 8", len(snap.Inventory.Drives))
	}
	for _, d := range snap.Inventory.Drives {
		if d.TemperatureCelsius != nil {
			t.Errorf("bay %s: TemperatureCelsius = %d, want nil (the BMC sent 0, which is not a reading)", d.Location, *d.TemperatureCelsius)
		}
		if d.PowerOnHours != nil {
			t.Errorf("bay %s: PowerOnHours = %d, want nil (the BMC sent null)", d.Location, *d.PowerOnHours)
		}
		if d.SSDEnduranceUsedPercent != nil {
			t.Errorf("bay %s: SSDEnduranceUsedPercent = %d, want nil (the BMC sent null)", d.Location, *d.SSDEnduranceUsedPercent)
		}
	}
}

// TestDiskDriveCountersDecode covers the other side of the guard: a real
// non-zero value is published, so "nil everywhere" cannot pass the test
// above by construction.
func TestDiskDriveCountersDecode(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		temp  *int32
		hours *int32
		endur *int32
	}{
		{"zero is not reported", `{"CurrentTemperatureCelsius":0,"PowerOnHours":0,"SSDEnduranceUtilizationPercentage":0}`, nil, nil, nil},
		{"null is not reported", `{"CurrentTemperatureCelsius":null,"PowerOnHours":null,"SSDEnduranceUtilizationPercentage":null}`, nil, nil, nil},
		{"absent is not reported", `{}`, nil, nil, nil},
		{"negative is not reported", `{"CurrentTemperatureCelsius":-1}`, nil, nil, nil},
		{"real values are published", `{"CurrentTemperatureCelsius":38,"PowerOnHours":41234,"SSDEnduranceUtilizationPercentage":3}`, new(int32(38)), new(int32(41234)), new(int32(3))},
		{"a float does not fail the decode", `{"CurrentTemperatureCelsius":38.0}`, new(int32(38)), nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc diskDriveDoc
			if err := json.Unmarshal([]byte(tt.body), &doc); err != nil {
				t.Fatalf("decode: %v", err)
			}
			checkPtr(t, "temperature", reported(doc.CurrentTemperatureCelsius), tt.temp)
			checkPtr(t, "power-on hours", reported(doc.PowerOnHours), tt.hours)
			checkPtr(t, "endurance", reported(doc.SSDEnduranceUtilizationPercentage), tt.endur)
		})
	}
}

func checkPtr(t *testing.T, what string, got, want *int32) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil:
		t.Errorf("%s = nil, want %d", what, *want)
	case want == nil:
		t.Errorf("%s = %d, want nil", what, *got)
	case *got != *want:
		t.Errorf("%s = %d, want %d", what, *got, *want)
	}
}

// TestRealArrayControllerIsPublished pins the controller fields on the
// captured P440ar, which runs in HBA mode with no cache battery.
func TestRealArrayControllerIsPublished(t *testing.T) {
	snap := probeRealSmartStorage(t)
	if len(snap.Inventory.StorageControllers) != 1 {
		t.Fatalf("StorageControllers = %d, want 1", len(snap.Inventory.StorageControllers))
	}
	sc := snap.Inventory.StorageControllers[0]
	want := StorageController{
		Location:          "Slot 0",
		Model:             "Smart Array P440ar Controller",
		Firmware:          "7.00",
		Health:            "OK",
		BackupPowerStatus: "NotPresent",
		CacheStatus:       "",
	}
	if sc != want {
		t.Errorf("StorageController = %+v, want %+v", sc, want)
	}
}

func TestCacheModuleStatusShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"absent", `{}`, ""},
		{"string", `{"CacheModuleStatus":"OK"}`, "OK"},
		{"object with Health", `{"CacheModuleStatus":{"Health":"Warning"}}`, "Warning"},
		{"unexpected shape", `{"CacheModuleStatus":42}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc arrayControllerDoc
			if err := json.Unmarshal([]byte(tt.body), &doc); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := doc.cacheStatus(); got != tt.want {
				t.Errorf("cacheStatus = %q, want %q", got, tt.want)
			}
		})
	}
}
