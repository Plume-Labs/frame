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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldGuessedClearLogTarget is the string template ClearLog's POST target
// used to be built from, before it was discovered from the machine's own
// LogService document
// ("/redfish/v1/Systems/%s/LogServices/IML/Actions/LogService.ClearLog/"
// with id "1"). It happens to be byte-identical to this hardware's real
// target (testdata/ilo4-real/systems_1_logservices_iml.json's
// Actions.#LogService.ClearLog.target), which is precisely why
// TestClearLogPostsTheDiscoveredTarget must not use the real fixture
// unmodified: a test server that only ever registers a handler at this one
// path can't tell a client that discovered it apart from one that guessed
// it and got lucky. See that test for how this is used to force the two
// apart.
const oldGuessedClearLogTarget = "/redfish/v1/Systems/1/LogServices/IML/Actions/LogService.ClearLog/"

// TestClearLogPostsTheDiscoveredTarget is the parked item this lot promotes
// to a fix: ClearLog's POST target used to be built from oldGuessedClearLogTarget's
// string template rather than discovered from the machine. Round-2 review
// found that on the real capture the two happen to coincide, so a server
// registering the handler only at that shared path can't distinguish
// discovery from a lucky guess — the test would pass against either
// implementation. This one moves the LogService document's advertised
// target to a different path a guess could never produce
// (discoveredClearLogTarget below) and registers the POST handler only
// there; the old guessed path gets its own handler that fails the test if
// it is ever hit, so a regression back to the string-template
// implementation fails this test instead of passing it by coincidence.
func TestClearLogPostsTheDiscoveredTarget(t *testing.T) {
	const discoveredClearLogTarget = "/redfish/v1/Systems/1/Oem/Hp/LogServices/IML/Actions/LogService.ClearLog/"

	logServiceBody, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_logservices_iml.json"))
	if err != nil {
		t.Fatalf("fixture systems_1_logservices_iml.json: %v", err)
	}
	if !strings.Contains(string(logServiceBody), oldGuessedClearLogTarget) {
		t.Fatalf("fixture no longer contains %q — oldGuessedClearLogTarget is stale", oldGuessedClearLogTarget)
	}
	movedLogServiceBody := strings.Replace(string(logServiceBody), oldGuessedClearLogTarget, discoveredClearLogTarget, 1)

	routes := realRoutes("postcomplete")
	delete(routes, "/redfish/v1/Systems/1/LogServices/IML/")
	mux := http.NewServeMux()
	for p, file := range routes {
		body, ferr := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
		if ferr != nil {
			t.Fatalf("fixture %s: %v", file, ferr)
		}
		mux.HandleFunc(p+"{$}", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	mux.HandleFunc("/redfish/v1/Systems/1/LogServices/IML/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(movedLogServiceBody))
	})

	posted := make(chan struct{}, 1)
	mux.HandleFunc(discoveredClearLogTarget+"{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		posted <- struct{}{}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(oldGuessedClearLogTarget+"{$}", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("POST landed on the old guessed template path %q instead of the discovered one %q — "+
			"ClearLog regressed to building its target from a string template", oldGuessedClearLogTarget, discoveredClearLogTarget)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	if err := insecureClient(srv.URL).ClearLog(context.Background()); err != nil {
		t.Fatalf("ClearLog: %v", err)
	}
	select {
	case <-posted:
	default:
		t.Fatal("ClearLog did not POST to the discovered target")
	}
}

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
	routes := map[string]string{
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
		// The system document's Oem.Hp.links.SmartStorage.href is the same
		// literal path regardless of power state, and every state's system
		// document advertises it — so Probe now always walks this tree, and
		// a route must exist here or these state tests fail on a 404 that
		// has nothing to do with what they're testing. There is no
		// per-power-state SmartStorage capture (see PROVENANCE.md's
		// "État dégradé" section: it was a one-off, non-reproducible
		// capture taken while the machine was off), so the degraded set is
		// reused here; none of the tests driven by realRoutes assert on
		// Inventory.Drives.
		"/redfish/v1/Systems/1/SmartStorage/":                               "smartstorage_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/":              "smartstorage_arraycontrollers_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/":            "smartstorage_arraycontrollers_0_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/": "smartstorage_diskdrives_degraded.json",
	}
	for i := 0; i < 8; i++ {
		routes[fmt.Sprintf("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/%d/", i)] =
			fmt.Sprintf("smartstorage_diskdrive_%d_degraded.json", i)
	}
	return routes
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
// (SizeMB/DIMMType/SocketLocator), not the DMTF one (CapacityMiB/
// MemoryDeviceType/DeviceLocator) this lot originally decoded.
//
// Slot is pinned to the human-readable SocketLocator form ("PROC 1 DIMM 1"),
// not the member Id ("proc1dimm1") — round 1 review (task-6b-report.md):
// this value is read by someone about to physically pull a DIMM out of a
// chassis, and the label the hardware itself prints is what belongs on
// screen, not an identifier they would have to decode first. Keyed by
// SizeMiB rather than Slot here, since Slot is the thing under test.
func TestProbeDecodesTheOlderHPMemorySchema(t *testing.T) {
	srv := serveRealFixtures(t, "postcomplete")
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	bySize := make(map[int32]redfishMemoryModuleForTest)
	for _, m := range snap.Inventory.MemoryModules {
		bySize[m.SizeMiB] = redfishMemoryModuleForTest{slot: m.Slot, typ: m.Type}
	}

	dimm1, ok := bySize[16384]
	if !ok {
		t.Fatalf("no 16384 MiB memory module in %+v", snap.Inventory.MemoryModules)
	}
	if dimm1.slot != "PROC 1 DIMM 1" {
		t.Errorf("16 GiB module Slot = %q, want the human-readable SocketLocator %q", dimm1.slot, "PROC 1 DIMM 1")
	}
	if dimm1.typ != "DDR4" {
		t.Errorf("16 GiB module Type = %q, want DDR4", dimm1.typ)
	}

	dimm2, ok := bySize[8192]
	if !ok {
		t.Fatalf("no 8192 MiB memory module in %+v", snap.Inventory.MemoryModules)
	}
	if dimm2.slot != "PROC 1 DIMM 2" {
		t.Errorf("8 GiB module Slot = %q, want the human-readable SocketLocator %q", dimm2.slot, "PROC 1 DIMM 2")
	}
	// Not every one of the six populated slots the collection lists was
	// captured (see realRoutes) — only that the two that were decode
	// correctly, and that a missing member does not fail the probe.
}

type redfishMemoryModuleForTest struct {
	slot string
	typ  string
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

// C1: the captured iLO4's IML Entries collection does not put entry bodies
// under "Members" — Members there are "@odata.id" link stubs — it puts them
// under a top-level "Items" array (HP's legacy REST collection shape,
// MemberType "LogEntry.1"). No test before this one ever asserted on
// snap.Log, snap.LogCounts or snap.LogTotal against the real capture, which
// is how a decoder built for the DMTF shape (Members-as-bodies) shipped
// through twelve reviews while producing 25 blank entries and a LogCounts
// of {"": 30} against real hardware. This asserts a specific entry's id,
// severity, message and timestamp — not just a count — precisely because a
// count-only assertion (LogTotal == 30, or len(Log) == 25) would have kept
// passing against the fully-blank-entries bug: 25 blank LogEntry values is
// still 25 entries.
func TestProbeDecodesTheRealIMLEntriesFromItemsNotMembers(t *testing.T) {
	srv := serveRealFixtures(t, "postcomplete")
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// The capture's Members@odata.count is 175 against 30 returned members —
	// LogTotal is the machine's own count, not what came back on this page
	// (see I2 and readLog's doc comment).
	if snap.LogTotal != 175 {
		t.Errorf("LogTotal = %d, want 175 (the machine's own count, not the 30-entry page)", snap.LogTotal)
	}
	if len(snap.Log) != EventLogRetainCount {
		t.Fatalf("len(Log) = %d, want %d", len(snap.Log), EventLogRetainCount)
	}

	// Newest-first: of the 30 returned entries (ids 1-30, dated 2021-2022),
	// id 29 carries the latest Created timestamp in the set.
	newest := snap.Log[0]
	if newest.ID != "29" {
		t.Errorf("Log[0].ID = %q, want %q", newest.ID, "29")
	}
	if newest.Severity != "Warning" {
		t.Errorf("Log[0].Severity = %q, want %q", newest.Severity, "Warning")
	}
	wantMessage := "Smart Storage Battery has exceeded the maximum amount of devices supported " +
		"(Battery 1, service information: 0x07). Action: 1. Remove additional devices. " +
		"2. Consult server troubleshooting guide. 3. Gather AHS log and contact Support"
	if newest.Message != wantMessage {
		t.Errorf("Log[0].Message = %q, want %q", newest.Message, wantMessage)
	}
	wantCreated := "2022-10-10T17:38:00Z"
	if got := newest.Created.UTC().Format("2006-01-02T15:04:05Z"); got != wantCreated {
		t.Errorf("Log[0].Created = %q, want %q", got, wantCreated)
	}

	// {"": 30} was the pre-fix LogCounts against this capture — every
	// Severity read off a blank-decoded Members stub. The real breakdown of
	// the 30 returned entries is Warning:20, OK:7, Critical:3.
	if _, blank := snap.LogCounts[""]; blank {
		t.Errorf("LogCounts has a blank severity key: %v", snap.LogCounts)
	}
	wantCounts := map[string]int{"Warning": 20, "OK": 7, "Critical": 3}
	for severity, want := range wantCounts {
		if snap.LogCounts[severity] != want {
			t.Errorf("LogCounts[%q] = %d, want %d (LogCounts = %v)", severity, snap.LogCounts[severity], want, snap.LogCounts)
		}
	}
}

// C2: chassis_1_thermal_postcomplete.json carries 46 temperature entries, 21
// of them Status.State "Absent" — empty sockets, not live sensors — and 23
// carrying UpperThresholdCritical: 0, including two live ones ("10-P/S 1"
// and "11-P/S 2", both Health OK at 40 C). Before this fix, readThermal
// appended every entry regardless of Status.State (producing 21 phantom
// rows reading 0 with no health), and UpperThresholdCritical: 0 decoded into
// a non-nil *int32 that src/lib/machines.ts's temperatureSeverity reads as a
// real threshold, flagging both live 40 C readings critical against a
// stated max of 0. The same chassis/8 fans carry 5 Absent entries too.
func TestProbeSkipsAbsentSensorsAndTreatsAZeroThresholdAsAbsent(t *testing.T) {
	srv := serveRealFixtures(t, "postcomplete")
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// 46 temperature entries, 21 Absent -> 25 remain. 8 fans, 5 Absent -> 3
	// remain.
	if got := len(snap.Sensors.Temperatures); got != 25 {
		t.Errorf("len(Temperatures) = %d, want 25 (46 captured minus 21 Absent)", got)
	}
	if got := len(snap.Sensors.Fans); got != 3 {
		t.Errorf("len(Fans) = %d, want 3 (8 captured minus 5 Absent)", got)
	}

	found := map[string]bool{}
	for _, temp := range snap.Sensors.Temperatures {
		if temp.Name == "10-P/S 1" || temp.Name == "11-P/S 2" {
			found[temp.Name] = true
			if temp.UpperCritical != nil {
				t.Errorf("%s: UpperCritical = %d, want nil (the capture's UpperThresholdCritical is 0, "+
					"not a real threshold)", temp.Name, *temp.UpperCritical)
			}
			if temp.Celsius != 40 || temp.Health != "OK" {
				t.Errorf("%s: Celsius=%d Health=%q, want 40/OK per the capture", temp.Name, temp.Celsius, temp.Health)
			}
		}
	}
	if !found["10-P/S 1"] || !found["11-P/S 2"] {
		t.Fatalf("expected both 10-P/S 1 and 11-P/S 2 among the non-Absent temperatures, found %v", found)
	}
}

// I4: chassisPath, systemPath and the system document's LogServices link are
// all "@odata.id" values this package reads back from the BMC's own JSON.
// decode.go and client.go used to reach Thermal, Power, the component
// collections and the log by concatenating a literal suffix onto them
// (chassisPath+"Thermal/", ODataID+"IML/Entries/", a hand-rebuilt
// "/redfish/v1/Systems/"+id+"/"+…) — correct only when that "@odata.id"
// happened to already carry a trailing slash, and a silent 404 (read as
// "this firmware doesn't expose it") the moment it didn't, per
// normalizePath's own doc comment. This server's Chassis, Systems and
// LogServices links are all missing their trailing slash on purpose, and
// every sub-resource route is registered the normal way (with one) — a
// Probe still built on concatenation would 404 against every one of them.
func TestProbeJoinsPathsThatArriveWithoutATrailingSlash(t *testing.T) {
	routes := map[string]string{
		"/redfish/v1/":                                   "service_root.json",
		"/redfish/v1/Chassis/1/Thermal/":                 "chassis_1_thermal.json",
		"/redfish/v1/Chassis/1/Power/":                   "chassis_1_power.json",
		"/redfish/v1/Systems/1/Processors/":              "processors.json",
		"/redfish/v1/Systems/1/Processors/1/":            "processor_1.json",
		"/redfish/v1/Systems/1/Processors/2/":            "processor_2.json",
		"/redfish/v1/Systems/1/Memory/":                  "memory.json",
		"/redfish/v1/Systems/1/Memory/1/":                "memory_1.json",
		"/redfish/v1/Systems/1/Memory/2/":                "memory_2.json",
		"/redfish/v1/Systems/1/EthernetInterfaces/":      "ethernet_interfaces.json",
		"/redfish/v1/Systems/1/EthernetInterfaces/1/":    "ethernet_interfaces_1.json",
		"/redfish/v1/Systems/1/EthernetInterfaces/2/":    "ethernet_interfaces_2.json",
		"/redfish/v1/Systems/1/LogServices/IML/Entries/": "log_entries.json",
		"/redfish/v1/Managers/1/":                        "managers_1.json",
	}
	mux := http.NewServeMux()
	for p, file := range routes {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(p+"{$}", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	// The three "@odata.id" values this test cares about, each missing its
	// trailing slash.
	mux.HandleFunc("/redfish/v1/Chassis/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Members@odata.count":1,"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`))
	})
	mux.HandleFunc("/redfish/v1/Systems/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Members@odata.count":1,"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`))
	})
	mux.HandleFunc("/redfish/v1/Systems/1/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Id":"1","PowerState":"On","IndicatorLED":"Off",` +
			`"Actions":{"#ComputerSystem.Reset":{"target":"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/"}},` +
			`"LogServices":{"@odata.id":"/redfish/v1/Systems/1/LogServices"}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(snap.Sensors.Temperatures) == 0 {
		t.Error("Temperatures empty — Chassis/1/Thermal/ was not reached")
	}
	if len(snap.Inventory.Processors) == 0 {
		t.Error("Processors empty — Systems/1/Processors/ was not reached")
	}
	if len(snap.Inventory.MemoryModules) == 0 {
		t.Error("MemoryModules empty — Systems/1/Memory/ was not reached")
	}
	if len(snap.Inventory.NetworkAdapters) == 0 {
		t.Error("NetworkAdapters empty — Systems/1/EthernetInterfaces/ was not reached")
	}
	if snap.LogTotal == 0 {
		t.Error("LogTotal = 0 — Systems/1/LogServices/IML/Entries/ was not reached")
	}
}

// movedSmartStorageRoot is a path the real machine does not use. A client
// that discovers SmartStorage from Oem.Hp.links.SmartStorage.href finds the
// drives here; one that hardcodes "/redfish/v1/Systems/1/SmartStorage/"
// finds a 404 and reports zero drives. Without this move both
// implementations pass, because on the captured hardware the guessed path
// and the advertised one are byte-identical — the same coincidence that
// hid the ClearLog defect (see TestClearLogPostsTheDiscoveredTarget).
const movedSmartStorageRoot = "/redfish/v1/Systems/1/Oem/Hp/SmartStorageRelocated/"

func TestSmartStorageRootIsDiscoveredNotGuessed(t *testing.T) {
	systemDoc, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_degraded.json"))
	if err != nil {
		t.Fatalf("fixture systems_1_degraded.json: %v", err)
	}
	moved := strings.ReplaceAll(
		string(systemDoc),
		"/redfish/v1/Systems/1/SmartStorage/",
		movedSmartStorageRoot,
	)

	guessed := make(chan struct{}, 1)
	mux := http.NewServeMux()
	serve := func(path, file string) {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(path+"{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	serve("/redfish/v1/", "service_root.json")
	serve("/redfish/v1/Systems/", "systems.json")
	mux.HandleFunc("/redfish/v1/Systems/1/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(moved))
	})
	serve(movedSmartStorageRoot, "smartstorage_degraded.json")
	serve("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/", "smartstorage_arraycontrollers_degraded.json")
	serve("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/", "smartstorage_arraycontrollers_0_degraded.json")
	serve("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/", "smartstorage_diskdrives_degraded.json")
	for i := 0; i < 8; i++ {
		serve(
			fmt.Sprintf("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/%d/", i),
			fmt.Sprintf("smartstorage_diskdrive_%d_degraded.json", i),
		)
	}
	// The guessed root gets its own handler so a regression is a failure,
	// not a silent 404 that merely yields zero drives.
	mux.HandleFunc("/redfish/v1/Systems/1/SmartStorage/{$}", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case guessed <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "u", "p", &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test server
	snap, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	select {
	case <-guessed:
		t.Fatal("client fetched the hardcoded SmartStorage path; it must read Oem.Hp.links.SmartStorage.href")
	default:
	}
	if len(snap.Inventory.Drives) != 8 {
		t.Fatalf("Drives = %d, want 8", len(snap.Inventory.Drives))
	}
}

// TestSmartStorageReadsTheSATADriveVerbatim pins the one drive the whole
// storage lot turns on: bay 2I:6:8, serial W4722RRA, the machine's only
// SATA disk and the only one Linux does not see. The BMC reports it
// Health OK — nothing in this document says it is masked — which is why
// the divergence in internal/storage.Join is computed from two sources
// and never read from one.
func TestSmartStorageReadsTheSATADriveVerbatim(t *testing.T) {
	srv := serveRealSmartStorage(t)
	c := New(srv.URL, "u", "p", &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test server
	snap, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	var got *Drive
	for i := range snap.Inventory.Drives {
		if snap.Inventory.Drives[i].SerialNumber == "W4722RRA" {
			got = &snap.Inventory.Drives[i]
		}
	}
	if got == nil {
		t.Fatalf("no drive with serial W4722RRA among %d drives", len(snap.Inventory.Drives))
	}
	if got.Location != "2I:6:8" {
		t.Errorf("Location = %q, want 2I:6:8", got.Location)
	}
	if got.Model != "MM1000GFJTE" {
		t.Errorf("Model = %q, want MM1000GFJTE", got.Model)
	}
	if got.Protocol != "SATA" {
		t.Errorf("Protocol = %q, want SATA (it is the machine's only SATA disk)", got.Protocol)
	}
	if got.MediaType != "HDD" {
		t.Errorf("MediaType = %q, want HDD", got.MediaType)
	}
	if got.SizeGB != 1000 {
		t.Errorf("SizeGB = %d, want 1000", got.SizeGB)
	}
	if got.Health != "OK" {
		t.Errorf("Health = %q, want OK — the BMC does not know this disk is masked", got.Health)
	}
	if len(got.StatusReasons) != 1 || got.StatusReasons[0] != "None" {
		t.Errorf("StatusReasons = %v, want [None]", got.StatusReasons)
	}
}

// realSmartStorageRoutes is the unmodified ilo4-real SmartStorage capture
// set. Returned as a fresh map so a caller can drop one document from it
// and have that one document 404 (serveFixturesFrom's catch-all).
func realSmartStorageRoutes() map[string]string {
	routes := map[string]string{
		"/redfish/v1/":                                                      "service_root.json",
		"/redfish/v1/Systems/":                                              "systems.json",
		"/redfish/v1/Systems/1/":                                            "systems_1_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/":                               "smartstorage_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/":              "smartstorage_arraycontrollers_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/":            "smartstorage_arraycontrollers_0_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/": "smartstorage_diskdrives_degraded.json",
	}
	for i := 0; i < 8; i++ {
		routes[fmt.Sprintf("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/%d/", i)] =
			fmt.Sprintf("smartstorage_diskdrive_%d_degraded.json", i)
	}
	return routes
}

// serveRealSmartStorage routes the unmodified ilo4-real SmartStorage
// captures, for tests about values rather than about discovery.
func serveRealSmartStorage(t *testing.T) *httptest.Server {
	t.Helper()
	return serveFixturesFrom(t, "ilo4-real", realSmartStorageRoutes())
}

// TestOneMissingDiskDocumentCostsOnlyThatDisk pins the final review's
// finding: a 404 on an individual disk document used to `return nil` out of
// readSmartStorage entirely, abandoning every controller and every drive
// after it — while the function's own comment claimed it stopped walking
// "that branch". The consequence is not a shorter list: internal/storage.Join
// reads a drive the BMC does not list as an os-only divergence, so one
// absent document renders every remaining disk on the machine as a
// divergence.
//
// The machine's eight drive documents are served except index 0, which
// 404s. Index 7 is W4722RRA, the SATA disk the whole storage lot turns on,
// and it sits *after* the hole — so it is present only if the walk
// continued. Asserting the count alone would not discriminate: a version
// that stopped at the hole returns zero drives, but so would several other
// bugs. The count AND the far-side drive together say the walk carried on.
func TestOneMissingDiskDocumentCostsOnlyThatDisk(t *testing.T) {
	routes := realSmartStorageRoutes()
	delete(routes, "/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/0/")
	srv := serveFixturesFrom(t, "ilo4-real", routes)

	snap, err := New(srv.URL, "u", "p", &tls.Config{InsecureSkipVerify: true}).Probe(context.Background()) //nolint:gosec // test server
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if len(snap.Inventory.Drives) != 7 {
		t.Errorf("got %d drives, want 7 — one 404 document must cost exactly one disk, not the rest of the walk",
			len(snap.Inventory.Drives))
	}
	var serials []string
	for _, d := range snap.Inventory.Drives {
		serials = append(serials, d.SerialNumber)
	}
	found := false
	for _, s := range serials {
		if s == "W4722RRA" {
			found = true
		}
		if s == "KZK245ZG" {
			t.Errorf("drive KZK245ZG is present, but its document 404d")
		}
	}
	if !found {
		t.Errorf("W4722RRA is missing from %v — it is drive 7, after the 404d document 0: the walk stopped instead of skipping",
			serials)
	}
}

// TestSmartStorageRootNotFoundLeavesDrivesEmptyButProbeSucceeds is fix
// round 1's regression test: readSmartStorage used to wrap any c.get error,
// including a plain 404, as fatal to the whole Probe. Every sibling reader
// in this package (readManager, readSensors, readThermal, readPower)
// tolerates a 404 and degrades gracefully instead — a tree that is
// advertised but 404s is a machine this client cannot fully read, not a
// probe failure. This test drives the SmartStorage root itself to 404 and
// asserts both halves of the fix: Drives ends up empty, AND the rest of the
// snapshot (power state, at minimum) is still populated. Asserting only the
// first half would not distinguish "tolerates not-found" from "swallows
// every error" — see TestSmartStorageRootServerErrorFailsProbe for the
// other half of that distinction.
func TestSmartStorageRootNotFoundLeavesDrivesEmptyButProbeSucceeds(t *testing.T) {
	systemDoc, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_degraded.json"))
	if err != nil {
		t.Fatalf("fixture systems_1_degraded.json: %v", err)
	}

	mux := http.NewServeMux()
	serve := func(path, file string) {
		body, ferr := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
		if ferr != nil {
			t.Fatalf("fixture %s: %v", file, ferr)
		}
		mux.HandleFunc(path+"{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	serve("/redfish/v1/", "service_root.json")
	serve("/redfish/v1/Systems/", "systems.json")
	mux.HandleFunc("/redfish/v1/Systems/1/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(systemDoc)
	})
	// The system document advertises a SmartStorage root, but it 404s — the
	// tree is advertised and absent, exactly the case this fix must
	// tolerate.
	mux.HandleFunc("/redfish/v1/Systems/1/SmartStorage/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(snap.Inventory.Drives) != 0 {
		t.Errorf("Drives = %v, want empty (SmartStorage root 404d)", snap.Inventory.Drives)
	}
	// The whole point of the fix: a 404 on one OEM sub-tree must not take
	// the rest of the snapshot down with it.
	if snap.PowerState != "Off" {
		t.Errorf("PowerState = %q, want %q — a SmartStorage 404 must not blank the rest of the snapshot", snap.PowerState, "Off")
	}
}

// TestSmartStorageRootServerErrorFailsProbe is the other half of the
// distinction TestSmartStorageRootNotFoundLeavesDrivesEmptyButProbeSucceeds
// needs: a 404 is tolerated, but a real failure (here, a 500) is not. A
// version of readSmartStorage that swallowed every c.get error, not just
// errNotFound, would pass the 404 test and this one both — this test is
// what tells them apart.
func TestSmartStorageRootServerErrorFailsProbe(t *testing.T) {
	systemDoc, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_degraded.json"))
	if err != nil {
		t.Fatalf("fixture systems_1_degraded.json: %v", err)
	}

	mux := http.NewServeMux()
	serve := func(path, file string) {
		body, ferr := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
		if ferr != nil {
			t.Fatalf("fixture %s: %v", file, ferr)
		}
		mux.HandleFunc(path+"{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	serve("/redfish/v1/", "service_root.json")
	serve("/redfish/v1/Systems/", "systems.json")
	mux.HandleFunc("/redfish/v1/Systems/1/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(systemDoc)
	})
	// The system document advertises a SmartStorage root, and this time it
	// answers with a server error rather than a 404 — a real failure, which
	// must still fail Probe.
	mux.HandleFunc("/redfish/v1/Systems/1/SmartStorage/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	if _, err := insecureClient(srv.URL).Probe(context.Background()); err == nil {
		t.Fatal("Probe succeeded, want an error — the SmartStorage root answered 500, not 404")
	}
}
