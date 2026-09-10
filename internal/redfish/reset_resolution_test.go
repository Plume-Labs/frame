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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Finding 2: GracefulShutdown is not itself a Redfish ResetType on the
// captured hardware. These three tests are the branches resolveResetType
// (client.go) has to get right. resetServer, the shared fixture-plus-Reset-
// handler helper, lives in client_test.go beside
// TestResetPostsTheActionTarget.

// The synthetic case: a machine whose AllowableValues does list
// GracefulShutdown sends it unchanged.
func TestResetSendsGracefulShutdownWhenTheMachineListsIt(t *testing.T) {
	doc := `{
		"Id": "1", "PowerState": "On", "IndicatorLED": "Off",
		"Actions": {"#ComputerSystem.Reset": {
			"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/",
			"ResetType@Redfish.AllowableValues": ["On", "GracefulShutdown", "ForceOff"]
		}},
		"LogServices": {"@odata.id": "/redfish/v1/Systems/1/LogServices/"}
	}`
	srv, posted := resetServer(t, doc)
	if err := insecureClient(srv.URL).Reset(context.Background(), "GracefulShutdown"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if v := <-posted; v != "GracefulShutdown" {
		t.Errorf("ResetType = %q, want GracefulShutdown", v)
	}
}

// The real case: the captured iLO4's AllowableValues is On, ForceOff,
// ForceRestart, Nmi, PushPowerButton — no GracefulShutdown — so a
// GracefulShutdown request must resolve to PushPowerButton, the ACPI request
// an installed OS acts on. Any of the three power-state captures carries the
// same AllowableValues; poweroff is used here without that being significant.
func TestResetFallsBackToPushPowerButtonWhenGracefulShutdownIsNotListed(t *testing.T) {
	posted := make(chan string, 1)
	routes := realRoutes("poweroff")
	mux := http.NewServeMux()
	for path, file := range routes {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(path+"{$}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	}
	mux.HandleFunc("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/{$}", func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			ResetType string `json:"ResetType"`
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		posted <- got.ResetType
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	if err := insecureClient(srv.URL).Reset(context.Background(), "GracefulShutdown"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if v := <-posted; v != "PushPowerButton" {
		t.Errorf("ResetType = %q, want PushPowerButton", v)
	}
}

// Neither GracefulShutdown nor its ACPI substitute is listed: Reset must
// refuse rather than send a string the machine will reject, or silently pick
// something the caller didn't ask for.
func TestResetReturnsErrUnsupportedWhenNeitherGracefulShutdownNorPushPowerButtonIsListed(t *testing.T) {
	doc := `{
		"Id": "1", "PowerState": "On", "IndicatorLED": "Off",
		"Actions": {"#ComputerSystem.Reset": {
			"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/",
			"ResetType@Redfish.AllowableValues": ["On", "ForceOff"]
		}},
		"LogServices": {"@odata.id": "/redfish/v1/Systems/1/LogServices/"}
	}`
	srv, _ := resetServer(t, doc)
	err := insecureClient(srv.URL).Reset(context.Background(), "GracefulShutdown")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// AllowableResetTypes used to be consulted only for GracefulShutdown; every
// other requested type passed straight through regardless of what the
// machine actually listed. C1 and C2 are evidence this firmware's Actions
// diverge from assumption more broadly than that, so all four actions the
// controller can request (On, GracefulShutdown, ForceOff, ForceRestart) are
// now checked: a type the machine's own AllowableValues doesn't list is
// refused before it is ever sent.
func TestResetRefusesATypeTheMachineDoesNotList(t *testing.T) {
	doc := `{
		"Id": "1", "PowerState": "On", "IndicatorLED": "Off",
		"Actions": {"#ComputerSystem.Reset": {
			"target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/",
			"ResetType@Redfish.AllowableValues": ["ForceOff", "ForceRestart"]
		}},
		"LogServices": {"@odata.id": "/redfish/v1/Systems/1/LogServices/"}
	}`
	srv, _ := resetServer(t, doc)
	err := insecureClient(srv.URL).Reset(context.Background(), "On")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// A firmware that never populates AllowableValues at all (the synthetic
// ilo4 fixture, used here via resetServer's default fullRoutes()) has
// nothing for resolveResetType to check a request against, so every request
// must still pass through unchecked — refusing everything an
// AllowableValues-less firmware doesn't explicitly enumerate would make
// Reset permanently unusable against it.
func TestResetPassesThroughWhenTheMachineListsNoAllowableValuesAtAll(t *testing.T) {
	srv, posted := resetServer(t, "")
	if err := insecureClient(srv.URL).Reset(context.Background(), "On"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if v := <-posted; v != "On" {
		t.Errorf("ResetType = %q, want On", v)
	}
}
