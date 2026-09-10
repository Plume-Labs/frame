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
	"net/http/httptest"
	"strings"
	"testing"
)

// This file holds the tests driven by the real ilo4-real captures — see
// testdata/ilo4-real/PROVENANCE.md — for Findings 1, 3 and 4. It is split
// out of client_test.go to keep that file, and this one, well under this
// lot's line ceiling.

// realRoutes wires the real ilo4-real captures for one of the three power
// states PROVENANCE.md recorded ("poweroff", "inpost", "postcomplete").
// Memory, processor, ethernet, log and manager fixtures are not suffixed by
// state — they were captured once and are the same document regardless of
// power state — so only the system document and chassis Thermal/Power vary
// by state here.
func realRoutes(state string) map[string]string {
	return map[string]string{
		"/redfish/v1/":                             "service_root.json",
		"/redfish/v1/Systems/":                     "systems.json",
		"/redfish/v1/Systems/1/":                   "systems_1_" + state + ".json",
		"/redfish/v1/Chassis/":                     "chassis.json",
		"/redfish/v1/Chassis/1/":                   "chassis_1.json",
		"/redfish/v1/Chassis/1/Thermal/":           "chassis_1_thermal_" + state + ".json",
		"/redfish/v1/Chassis/1/Power/":             "chassis_1_power_" + state + ".json",
		"/redfish/v1/Managers/1/":                  "managers_1.json",
		"/redfish/v1/Systems/1/Processors/":        "systems_1_processors.json",
		"/redfish/v1/Systems/1/Processors/1/":      "systems_1_processors_1.json",
		"/redfish/v1/Systems/1/Memory/":            "systems_1_memory.json",
		"/redfish/v1/Systems/1/Memory/proc1dimm1/": "systems_1_memory_proc1dimm1.json",
		"/redfish/v1/Systems/1/Memory/proc1dimm2/": "systems_1_memory_proc1dimm2.json",
		// The collection also lists proc1dimm4/5/9/12 (see PROVENANCE.md:
		// six of twelve slots are populated on this machine), which were not
		// captured. The 404 catch-all below answers for them, and readMemory
		// already tolerates a 404 on an individual member by skipping it —
		// the same tolerance the sparse-firmware tests elsewhere in this
		// package depend on.
		"/redfish/v1/Systems/1/EthernetInterfaces/":      "systems_1_ethernetinterfaces.json",
		"/redfish/v1/Systems/1/EthernetInterfaces/1/":    "systems_1_ethernetinterfaces_1.json",
		"/redfish/v1/Systems/1/LogServices/":             "systems_1_logservices.json",
		"/redfish/v1/Systems/1/LogServices/IML/":         "systems_1_logservices_iml.json",
		"/redfish/v1/Systems/1/LogServices/IML/Entries/": "systems_1_logservices_iml_entries.json",
	}
}

func serveRealFixtures(t *testing.T, state string) *httptest.Server {
	t.Helper()
	return serveFixturesFrom(t, "ilo4-real", realRoutes(state))
}

// Finding 1: the captured iLO4 replays cached Thermal/Power readings as live
// ones. PowerState and Oem.Hp.PostState are the only reliable source of
// whether a reading describes the machine now.
func TestProbeMarksSensorsUntrustworthyOutsideAFinishedPost(t *testing.T) {
	cases := []struct {
		state       string
		wantPower   string
		wantPost    string
		wantTrusted bool
	}{
		{"poweroff", "Off", "PowerOff", false},
		{"inpost", "On", "InPost", false},
		{"postcomplete", "On", "InPostDiscoveryComplete", true},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			srv := serveRealFixtures(t, tc.state)
			snap, err := insecureClient(srv.URL).Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if snap.PowerState != tc.wantPower {
				t.Errorf("PowerState = %q, want %q", snap.PowerState, tc.wantPower)
			}
			if snap.PostState != tc.wantPost {
				t.Errorf("PostState = %q, want %q", snap.PostState, tc.wantPost)
			}
			if snap.SensorsTrustworthy != tc.wantTrusted {
				t.Errorf("SensorsTrustworthy = %v, want %v", snap.SensorsTrustworthy, tc.wantTrusted)
			}
		})
	}
}

// The discriminating case: the temperature is identical in all three states,
// so a test that only checked the reading would pass against code that
// trusted it.
func TestTheCachedTemperatureIsIdenticalInEveryPowerState(t *testing.T) {
	var readings []int32
	for _, state := range []string{"poweroff", "inpost", "postcomplete"} {
		srv := serveRealFixtures(t, state)
		snap, err := insecureClient(srv.URL).Probe(context.Background())
		if err != nil {
			t.Fatalf("Probe(%s): %v", state, err)
		}
		for _, temp := range snap.Sensors.Temperatures {
			if strings.Contains(temp.Name, "CPU") {
				readings = append(readings, temp.Celsius)
				break
			}
		}
	}
	if len(readings) != 3 {
		t.Fatalf("expected a CPU reading in each state, got %v", readings)
	}
	if !(readings[0] == readings[1] && readings[1] == readings[2]) {
		t.Fatalf("readings differ across power states (%v) — the premise of this lot's "+
			"trustworthiness flag no longer holds and Finding 1 must be re-derived", readings)
	}
}

// Finding 3: the captured iLO4 serves the older HPE Memory schema
// (SizeMB/DIMMType/member-Id), not the DMTF one (CapacityMiB/
// MemoryDeviceType/DeviceLocator) this lot originally decoded.
func TestProbeDecodesTheOlderHPMemorySchema(t *testing.T) {
	srv := serveRealFixtures(t, "postcomplete")
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	byModel := make(map[string]redfishMemoryModuleForTest)
	for _, m := range snap.Inventory.MemoryModules {
		byModel[m.Slot] = redfishMemoryModuleForTest{sizeMiB: m.SizeMiB, typ: m.Type}
	}
	dimm1, ok := byModel["proc1dimm1"]
	if !ok {
		t.Fatalf("no memory module with Slot proc1dimm1 in %+v", snap.Inventory.MemoryModules)
	}
	if dimm1.sizeMiB != 16384 {
		t.Errorf("proc1dimm1 SizeMiB = %d, want 16384", dimm1.sizeMiB)
	}
	if dimm1.typ != "DDR4" {
		t.Errorf("proc1dimm1 Type = %q, want DDR4", dimm1.typ)
	}

	dimm2, ok := byModel["proc1dimm2"]
	if !ok {
		t.Fatalf("no memory module with Slot proc1dimm2 in %+v", snap.Inventory.MemoryModules)
	}
	if dimm2.sizeMiB != 8192 {
		t.Errorf("proc1dimm2 SizeMiB = %d, want 8192", dimm2.sizeMiB)
	}
	// Not every one of the six populated slots the collection lists was
	// captured (see realRoutes) — only that the two that were decode
	// correctly, and that a missing member does not fail the probe.
}

type redfishMemoryModuleForTest struct {
	sizeMiB int32
	typ     string
}

// Finding 4: the captured iLO4 answers a path missing its trailing slash
// with a 308 rather than the resource. This proves the client asks for the
// resource directly rather than depending on that redirect: the test server
// only ever registers the trailing-slash route, so a client that failed to
// normalize would 404 against the catch-all instead of reaching
// service_root.json.
func TestGetNormalizesAPathMissingItsTrailingSlash(t *testing.T) {
	srv := serveFixtures(t, map[string]string{"/redfish/v1/": "service_root.json"})
	c, ok := insecureClient(srv.URL).(*client)
	if !ok {
		t.Fatalf("insecureClient returned %T, want *client", insecureClient(srv.URL))
	}

	var root serviceRootJSON
	if err := c.get(context.Background(), "/redfish/v1", &root); err != nil {
		t.Fatalf("get without trailing slash: %v", err)
	}
	if root.Systems.ODataID != "/redfish/v1/Systems/" {
		t.Errorf("Systems = %q", root.Systems.ODataID)
	}
}
