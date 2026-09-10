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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// serveFixtures maps Redfish paths onto the recorded files. Each route is
// registered anchored ("{$}") so it matches only its exact path: Go's
// ServeMux otherwise treats a pattern ending in "/" as a subtree match,
// which would silently absorb requests to any deeper, unmapped path (e.g.
// a route for "/redfish/v1/Chassis/" would also answer for
// "/redfish/v1/Chassis/1/Thermal/") and mask the 404 the sparse-firmware
// tests depend on. The catch-all below is what actually produces that 404:
// a firmware that does not expose a resource simply does not answer for it.
func serveFixtures(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, file := range routes {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(path+"{$}", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fullRoutes() map[string]string {
	return map[string]string{
		"/redfish/v1/":                                   "service_root.json",
		"/redfish/v1/Systems/":                           "systems.json",
		"/redfish/v1/Systems/1/":                         "system_1.json",
		"/redfish/v1/Chassis/":                           "chassis.json",
		"/redfish/v1/Chassis/1/Thermal/":                 "chassis_1_thermal.json",
		"/redfish/v1/Chassis/1/Power/":                   "chassis_1_power.json",
		"/redfish/v1/Systems/1/LogServices/IML/Entries/": "log_entries.json",
		"/redfish/v1/Managers/1/":                        "managers_1.json",
		"/redfish/v1/Systems/1/Processors/":              "processors.json",
		"/redfish/v1/Systems/1/Processors/1/":            "processor_1.json",
		"/redfish/v1/Systems/1/Processors/2/":            "processor_2.json",
		"/redfish/v1/Systems/1/Memory/":                  "memory.json",
		"/redfish/v1/Systems/1/Memory/1/":                "memory_1.json",
		"/redfish/v1/Systems/1/Memory/2/":                "memory_2.json",
		"/redfish/v1/Systems/1/EthernetInterfaces/":      "ethernet_interfaces.json",
		"/redfish/v1/Systems/1/EthernetInterfaces/1/":    "ethernet_interfaces_1.json",
		"/redfish/v1/Systems/1/EthernetInterfaces/2/":    "ethernet_interfaces_2.json",
	}
}

func insecureClient(url string) Client {
	return New(url, "admin", "secret", &tls.Config{InsecureSkipVerify: true})
}

func TestProbeReadsInventorySensorsAndLog(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if snap.PowerState != "On" {
		t.Errorf("PowerState = %q, want On", snap.PowerState)
	}
	if snap.Inventory.Model != "ProLiant ML350 Gen9" {
		t.Errorf("Model = %q", snap.Inventory.Model)
	}
	if snap.Inventory.SerialNumber != "CZJ1234567" {
		t.Errorf("SerialNumber = %q", snap.Inventory.SerialNumber)
	}
	if snap.Inventory.BMCFirmware != "iLO 4 v2.82" {
		t.Errorf("BMCFirmware = %q, want the Managers value", snap.Inventory.BMCFirmware)
	}
	if snap.Inventory.TotalMemoryGiB != 64 {
		t.Errorf("TotalMemoryGiB = %d, want 64", snap.Inventory.TotalMemoryGiB)
	}
	if got := len(snap.Sensors.Temperatures); got != 3 {
		t.Errorf("temperatures = %d, want 3", got)
	}
	if snap.Sensors.PowerConsumedWatts != 118 {
		t.Errorf("PowerConsumedWatts = %d, want 118", snap.Sensors.PowerConsumedWatts)
	}
	if got := len(snap.Sensors.PowerSupplies); got != 2 {
		t.Errorf("power supplies = %d, want 2", got)
	}
	if got := len(snap.Inventory.Processors); got != 2 {
		t.Errorf("processors = %d, want 2", got)
	} else {
		if snap.Inventory.Processors[0].Model != "Intel(R) Xeon(R) CPU E5-2650 v4" {
			t.Errorf("processor model = %q", snap.Inventory.Processors[0].Model)
		}
		if snap.Inventory.Processors[0].Socket != "Proc 1" {
			t.Errorf("processor socket = %q, want Proc 1", snap.Inventory.Processors[0].Socket)
		}
		if snap.Inventory.Processors[0].Cores != 12 {
			t.Errorf("processor cores = %d, want 12", snap.Inventory.Processors[0].Cores)
		}
		if snap.Inventory.Processors[0].Threads != 24 {
			t.Errorf("processor threads = %d, want 24", snap.Inventory.Processors[0].Threads)
		}
	}
	if got := len(snap.Inventory.MemoryModules); got != 2 {
		t.Errorf("memory modules = %d, want 2", got)
	} else {
		if snap.Inventory.MemoryModules[0].Slot != "PROC 1 DIMM 1" {
			t.Errorf("memory module slot = %q", snap.Inventory.MemoryModules[0].Slot)
		}
		if snap.Inventory.MemoryModules[0].SizeMiB != 16384 {
			t.Errorf("memory module size = %d, want 16384", snap.Inventory.MemoryModules[0].SizeMiB)
		}
		if snap.Inventory.MemoryModules[0].Type != "DDR4" {
			t.Errorf("memory module type = %q, want DDR4", snap.Inventory.MemoryModules[0].Type)
		}
	}
	if got := len(snap.Inventory.NetworkAdapters); got != 2 {
		t.Errorf("network adapters = %d, want 2", got)
	} else {
		if snap.Inventory.NetworkAdapters[0].MAC != "B4:B5:2F:00:11:22" {
			t.Errorf("network adapter MAC = %q", snap.Inventory.NetworkAdapters[0].MAC)
		}
		if snap.Inventory.NetworkAdapters[0].Name != "Embedded LOM 1 Port 1" {
			t.Errorf("network adapter name = %q", snap.Inventory.NetworkAdapters[0].Name)
		}
	}
	// Drives is deliberately never populated by this package (see the
	// comment at its population site in client.go); a sparse or full probe
	// alike leaves it nil.
	if snap.Inventory.Drives != nil {
		t.Errorf("Drives = %v, want nil (not yet implemented)", snap.Inventory.Drives)
	}
}

// The fan reading is a percentage on this hardware and an RPM figure on
// others. A client that dropped Units would render "23" as a fan speed.
func TestProbeKeepsTheFanUnits(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(snap.Sensors.Fans) == 0 {
		t.Fatal("no fans")
	}
	if snap.Sensors.Fans[0].Units != "Percent" {
		t.Errorf("Units = %q, want Percent", snap.Sensors.Fans[0].Units)
	}
	if snap.Sensors.Fans[0].Reading != 23 {
		t.Errorf("Reading = %d, want 23", snap.Sensors.Fans[0].Reading)
	}
}

func TestProbeCountsTheWholeLogNotJustWhatItReturns(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if snap.LogTotal != 3 {
		t.Errorf("LogTotal = %d, want 3", snap.LogTotal)
	}
	if snap.LogCounts["Critical"] != 1 || snap.LogCounts["Warning"] != 1 || snap.LogCounts["OK"] != 1 {
		t.Errorf("LogCounts = %v", snap.LogCounts)
	}
}

// The named risk in the spec: iLO4 firmware revisions omit fields and whole
// resources. A probe against a sparse service must return what it found, not
// an error and not a panic.
func TestProbeToleratesAFirmwareThatOmitsResources(t *testing.T) {
	srv := serveFixtures(t, map[string]string{
		"/redfish/v1/":           "service_root.json",
		"/redfish/v1/Systems/":   "systems.json",
		"/redfish/v1/Systems/1/": "system_1_sparse.json",
		"/redfish/v1/Chassis/":   "chassis.json",
	})
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe on a sparse service: %v", err)
	}
	if snap.PowerState != "Off" {
		t.Errorf("PowerState = %q, want Off", snap.PowerState)
	}
	if snap.Inventory.SerialNumber != "" {
		t.Errorf("SerialNumber = %q, want empty on a sparse service", snap.Inventory.SerialNumber)
	}
	if len(snap.Sensors.Temperatures) != 0 {
		t.Errorf("temperatures = %d, want none", len(snap.Sensors.Temperatures))
	}
}

func TestProbeReportsAuthFailureDistinctly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	_, err := insecureClient(srv.URL).Probe(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// Verification is on unless the spec turns it off, and a self-signed BMC
// certificate must surface as ErrTLS rather than as a generic dial failure —
// the controller renders the two differently.
func TestProbeReportsTLSFailureDistinctly(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	verifying := New(srv.URL, "admin", "secret", &tls.Config{})
	_, err := verifying.Probe(context.Background())
	if !errors.Is(err, ErrTLS) {
		t.Fatalf("err = %v, want ErrTLS", err)
	}
}

func TestResetPostsTheActionTarget(t *testing.T) {
	var got struct {
		ResetType string `json:"ResetType"`
	}
	posted := make(chan string, 1)

	routes := fullRoutes()
	mux := http.NewServeMux()
	for path, file := range routes {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(path+"{$}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	}
	mux.HandleFunc("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		posted <- got.ResetType
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	if err := insecureClient(srv.URL).Reset(context.Background(), "GracefulShutdown"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if v := <-posted; v != "GracefulShutdown" {
		t.Errorf("ResetType = %q, want GracefulShutdown", v)
	}
}
