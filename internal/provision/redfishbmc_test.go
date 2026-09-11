package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRedfishBMCTestServer starts an httptest.Server driving handler and
// returns a RedfishBMC pointed at it. tlsConfig is always nil: httptest's
// default server is plain HTTP, and NewRedfishBMC's tlsConfig argument only
// ever matters to an https connection -- these tests exist to pin the
// request shape (method, path, body, auth), not TLS handling, which
// internal/redfish's own client already owns and tests.
func newRedfishBMCTestServer(t *testing.T, handler http.HandlerFunc) (*RedfishBMC, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewRedfishBMC(srv.URL, "admin", "s3cret", nil), srv
}

// TestRedfishBMCInsertMediaPostsTheHPOEMAction pins InsertMedia's request
// shape against the design's own comment: an HP OEM action, not the
// standard Redfish VirtualMedia.InsertMedia -- proven here by pinning the
// exact path, not merely that some POST happened.
func TestRedfishBMCInsertMediaPostsTheHPOEMAction(t *testing.T) {
	var gotMethod, gotPath, gotUser, gotPass, gotContentType string
	var gotBody []byte
	authOK := false

	b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotUser, gotPass, authOK = r.BasicAuth()
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	if err := b.InsertMedia(context.Background(), "http://provisiond/iso/tok.iso"); err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	const want = "/redfish/v1/Managers/1/VirtualMedia/2/Actions/Oem/Hp/HpiLOVirtualMedia.InsertVirtualMedia/"
	if gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
	if !authOK || gotUser != "admin" || gotPass != "s3cret" {
		t.Errorf("basic auth = %q:%q (ok=%v), want admin:s3cret", gotUser, gotPass, authOK)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}

	var decoded struct{ Image string }
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body %s did not decode: %v", gotBody, err)
	}
	if decoded.Image != "http://provisiond/iso/tok.iso" {
		t.Errorf("Image = %q, want the URL InsertMedia was called with", decoded.Image)
	}
}

// TestRedfishBMCMediaInsertedDecodesBothOutcomes is the positive control
// InsertMedia's own test cannot be: it drives both true and false through
// the same field, so an implementation that hardcoded either value fails
// exactly one iteration -- a single-value test would not catch a hardcoded
// "return true, nil".
func TestRedfishBMCMediaInsertedDecodesBothOutcomes(t *testing.T) {
	for _, want := range []bool{true, false} {
		want := want
		t.Run(fmt.Sprintf("inserted=%v", want), func(t *testing.T) {
			var gotMethod, gotPath string
			b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"Inserted": want, "Image": "http://provisiond/iso/tok.iso"})
			})

			got, err := b.MediaInserted(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("MediaInserted() = %v, want %v", got, want)
			}
			if gotMethod != http.MethodGet {
				t.Errorf("method = %s, want GET", gotMethod)
			}
			if gotPath != "/redfish/v1/Managers/1/VirtualMedia/2/" {
				t.Errorf("path = %s, want the VirtualMedia resource itself, not an action", gotPath)
			}
		})
	}
}

// TestRedfishBMCEjectMediaPostsTheMatchingHPOEMAction pins EjectMedia's
// request shape the same way InsertMedia's own test does.
func TestRedfishBMCEjectMediaPostsTheMatchingHPOEMAction(t *testing.T) {
	var gotMethod, gotPath string
	b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	if err := b.EjectMedia(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	const want = "/redfish/v1/Managers/1/VirtualMedia/2/Actions/Oem/Hp/HpiLOVirtualMedia.EjectVirtualMedia/"
	if gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
}

// TestRedfishBMCSetBootOnceSendsTheStandardBootOverrideProperties pins the
// design's other explicit claim (§5): boot mode is set through the
// standard Redfish BootSourceOverrideMode property, PATCHed on the system
// resource -- not an OEM action, unlike the two above. The full decoded
// body is asserted, not a substring match, so an implementation that also
// (wrongly) sent BootSourceOverrideEnabled: "Continuous" or omitted the
// mode entirely would be caught.
func TestRedfishBMCSetBootOnceSendsTheStandardBootOverrideProperties(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	if err := b.SetBootOnce(context.Background(), "Cd", "UEFI"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/redfish/v1/Systems/1/" {
		t.Errorf("path = %s, want the system resource itself", gotPath)
	}

	var decoded struct {
		Boot struct {
			BootSourceOverrideEnabled string
			BootSourceOverrideTarget  string
			BootSourceOverrideMode    string
		}
	}
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body %s did not decode: %v", gotBody, err)
	}
	if decoded.Boot.BootSourceOverrideEnabled != "Once" {
		t.Errorf("BootSourceOverrideEnabled = %q, want %q", decoded.Boot.BootSourceOverrideEnabled, "Once")
	}
	if decoded.Boot.BootSourceOverrideTarget != "Cd" {
		t.Errorf("BootSourceOverrideTarget = %q, want %q", decoded.Boot.BootSourceOverrideTarget, "Cd")
	}
	if decoded.Boot.BootSourceOverrideMode != "UEFI" {
		t.Errorf("BootSourceOverrideMode = %q, want %q", decoded.Boot.BootSourceOverrideMode, "UEFI")
	}
}

// TestRedfishBMCSetBootOncePassesModeThrough is SetBootOnce's own positive
// control on the mode argument specifically: both values install.go can
// pass (Options.BootMode is "UEFI" or "Legacy", never a third value) are
// driven through, so a version that hardcoded "UEFI" regardless of the
// argument fails on the second case.
func TestRedfishBMCSetBootOncePassesModeThrough(t *testing.T) {
	for _, mode := range []string{"UEFI", "Legacy"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			var gotBody []byte
			b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
			})
			if err := b.SetBootOnce(context.Background(), "Cd", mode); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(gotBody), `"BootSourceOverrideMode":"`+mode+`"`) {
				t.Errorf("body %s does not carry mode %q", gotBody, mode)
			}
		})
	}
}

// TestRedfishBMCClearBootOverrideDisablesWithoutSettingTargetOrMode proves
// ClearBootOverride's body is narrower than SetBootOnce's: only
// BootSourceOverrideEnabled: "Disabled". Asserting the target/mode keys are
// entirely absent (not merely unequal to some value) is what would catch an
// implementation that copy-pasted SetBootOnce's body and only patched the
// Enabled field, leaving a stale Target/Mode on the wire.
func TestRedfishBMCClearBootOverrideDisablesWithoutSettingTargetOrMode(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	if err := b.ClearBootOverride(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/redfish/v1/Systems/1/" {
		t.Errorf("path = %s, want the system resource itself", gotPath)
	}

	var decoded map[string]any
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body %s did not decode: %v", gotBody, err)
	}
	boot, ok := decoded["Boot"].(map[string]any)
	if !ok {
		t.Fatalf("body %s has no Boot object", gotBody)
	}
	if boot["BootSourceOverrideEnabled"] != "Disabled" {
		t.Errorf("BootSourceOverrideEnabled = %v, want Disabled", boot["BootSourceOverrideEnabled"])
	}
	if _, present := boot["BootSourceOverrideTarget"]; present {
		t.Errorf("body %s sets BootSourceOverrideTarget, which ClearBootOverride must leave alone", gotBody)
	}
	if _, present := boot["BootSourceOverrideMode"]; present {
		t.Errorf("body %s sets BootSourceOverrideMode, which ClearBootOverride must leave alone", gotBody)
	}
}

// TestRedfishBMCDoSurfacesANonSuccessStatus is do()'s own error-branch test:
// every write method above routes through it, and every one of them
// currently has zero coverage of what happens when the BMC answers with a
// non-2xx status. The response body is asserted present in the returned
// error, not merely that an error exists -- a caller reading a failed
// EjectMedia/InsertMedia/SetBootOnce needs the BMC's own diagnostic text,
// not just "it failed".
func TestRedfishBMCDoSurfacesANonSuccessStatus(t *testing.T) {
	b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "iLO is busy processing another request", http.StatusServiceUnavailable)
	})

	err := b.EjectMedia(context.Background())
	if err == nil {
		t.Fatal("want an error for a 503 response, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q does not mention the HTTP status", err.Error())
	}
	if !strings.Contains(err.Error(), "iLO is busy processing another request") {
		t.Errorf("error %q does not carry the BMC's own diagnostic body", err.Error())
	}
}

// TestRedfishBMCMediaInsertedSurfacesADecodeError proves the out!=nil decode
// path: a response that is not valid JSON must not be silently read back as
// MediaInserted() == false, which would be indistinguishable from the BMC
// truthfully reporting no media attached.
func TestRedfishBMCMediaInsertedSurfacesADecodeError(t *testing.T) {
	b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	})

	_, err := b.MediaInserted(context.Background())
	if err == nil {
		t.Fatal("want a decode error for a non-JSON response, got nil")
	}
}

// --- Serial and Reset: both delegate to lot 1's redfish.Client, so their
// own tests only need to prove the delegation actually happens -- the
// request shape underneath is internal/redfish's own, already covered by
// that package's tests against real ML350 Gen9 captures.

// redfishSystemFixtureServer serves the minimal document set
// redfish.Client needs to resolve the system resource: a service root
// naming a Systems collection, that collection naming one member, and the
// member's own document. Every other resource Probe walks (Chassis,
// Managers, LogServices) is left unadvertised, which Probe's own contract
// (internal/redfish/client.go) treats as "this firmware doesn't expose it"
// rather than an error -- exactly the tolerance this fixture leans on to
// stay minimal.
//
// onReset, when non-nil, is called with the raw request body of every POST
// to the Reset action target, letting a caller capture what Reset actually
// sent without re-implementing this fixture's plumbing.
func redfishSystemFixtureServer(t *testing.T, serial string, resetTarget string, allowable []string, onReset func([]byte)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Systems": map[string]string{"@odata.id": "/redfish/v1/Systems/"},
		})
	})
	mux.HandleFunc("/redfish/v1/Systems/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Members@odata.count": 1,
			"Members":             []map[string]string{{"@odata.id": "/redfish/v1/Systems/1/"}},
		})
	})
	mux.HandleFunc("/redfish/v1/Systems/1/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"SerialNumber": serial,
			"Actions": map[string]any{
				"#ComputerSystem.Reset": map[string]any{
					"target":                            resetTarget,
					"ResetType@Redfish.AllowableValues": allowable,
				},
			},
		})
	})
	mux.HandleFunc("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		if onReset != nil {
			b, _ := io.ReadAll(r.Body)
			onReset(b)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRedfishBMCSerialDelegatesToProbe(t *testing.T) {
	srv := redfishSystemFixtureServer(t, "CZ3xxxxxxx", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/", []string{"On", "ForceRestart"}, nil)
	b := NewRedfishBMC(srv.URL, "admin", "s3cret", nil)

	got, err := b.Serial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "CZ3xxxxxxx" {
		t.Errorf("Serial() = %q, want %q", got, "CZ3xxxxxxx")
	}
}

func TestRedfishBMCSerialSurfacesAProbeFailure(t *testing.T) {
	// A server answering nothing Redfish-shaped at /redfish/v1/ -- Probe's
	// own ErrUnsupported path -- is the positive control for the success
	// test above: it proves Serial() is not simply returning a fixed
	// string regardless of what Probe does.
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	b := NewRedfishBMC(srv.URL, "admin", "s3cret", nil)

	if _, err := b.Serial(context.Background()); err == nil {
		t.Fatal("want an error when Probe cannot reach a service root, got nil")
	}
}

func TestRedfishBMCResetDelegatesTheResetType(t *testing.T) {
	// onReset captures the request body, proving install.go's actual call
	// -- Reset(ctx, "ForceRestart") -- reaches the BMC as ResetType:
	// ForceRestart, not silently substituted the way GracefulShutdown
	// legitimately is elsewhere in this package (Finding 2).
	var gotBody []byte
	srv := redfishSystemFixtureServer(t, "CZ3xxxxxxx", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/",
		[]string{"On", "ForceOff", "ForceRestart"}, func(b []byte) { gotBody = b })
	b := NewRedfishBMC(srv.URL, "admin", "s3cret", nil)

	if err := b.Reset(context.Background(), "ForceRestart"); err != nil {
		t.Fatal(err)
	}
	var decoded struct{ ResetType string }
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body %s did not decode: %v", gotBody, err)
	}
	if decoded.ResetType != "ForceRestart" {
		t.Errorf("ResetType = %q, want %q", decoded.ResetType, "ForceRestart")
	}
}
