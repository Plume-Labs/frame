package provision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// These tests drive EjectMedia and ClearBootOverride against a fake that
// answers the two ways an iLO 4 (ML350 Gen9) answered them at the end of a
// real installation:
//
//   - POST .../VirtualMedia/2/Actions/Oem/Hp/HpiLOVirtualMedia.EjectVirtualMedia/
//     returned 500 while the virtual CD already read Inserted: false;
//   - PATCH /redfish/v1/Systems/1/ with Boot.BootSourceOverrideEnabled and
//     no Boot.BootSourceOverrideTarget returned 400 "BootSourceOverrideEnabled
//     cannot be set before BootSourceOverrideTarget is set".
//
// Both used to fail the whole installation in its cleanup, after the OS
// was already on disk.

const (
	fakeEjectPath   = fakeVMPath + "Actions/Oem/Hp/HpiLOVirtualMedia.EjectVirtualMedia/"
	enabledNeedsMsg = "BootSourceOverrideEnabled cannot be set before BootSourceOverrideTarget is set"
)

type ilo4CleanupFake struct {
	mu       sync.Mutex
	inserted bool
	// ejectFails answers the eject POST with 500 even when it did eject:
	// the media is gone afterwards, the answer says otherwise.
	ejectFails bool
	// ejectNoop answers the eject POST with 500 and leaves the media in.
	ejectNoop bool
	enabled   string
	target    string
	posts     int
	patches   []map[string]any
}

func (f *ilo4CleanupFake) serve(t *testing.T) *RedfishBMC {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return NewRedfishBMC(srv.URL, "admin", "s3cret", nil)
}

func (f *ilo4CleanupFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == fakeVMPath:
		_ = json.NewEncoder(w).Encode(map[string]any{"Inserted": f.inserted})
	case r.Method == http.MethodPost && r.URL.Path == fakeEjectPath:
		f.posts++
		switch {
		case !f.inserted:
			http.Error(w, `{"error":{"code":"iLO.0.10.ExtendedInfo","message":"See @Message.ExtendedInfo for more information."}}`, http.StatusInternalServerError)
		case f.ejectNoop:
			http.Error(w, "internal error", http.StatusInternalServerError)
		case f.ejectFails:
			f.inserted = false
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			f.inserted = false
			w.WriteHeader(http.StatusOK)
		}
	case r.Method == http.MethodGet && r.URL.Path == fakeSystemPath:
		_ = json.NewEncoder(w).Encode(map[string]any{"Boot": map[string]any{
			"BootSourceOverrideEnabled": f.enabled,
			"BootSourceOverrideTarget":  f.target,
		}})
	case r.Method == http.MethodPatch && r.URL.Path == fakeSystemPath:
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "malformed JSON", http.StatusBadRequest)
			return
		}
		f.patches = append(f.patches, body)
		boot, _ := body["Boot"].(map[string]any)
		en, hasEnabled := boot["BootSourceOverrideEnabled"].(string)
		tg, hasTarget := boot["BootSourceOverrideTarget"].(string)
		if hasEnabled && !hasTarget {
			http.Error(w, enabledNeedsMsg, http.StatusBadRequest)
			return
		}
		if hasTarget {
			f.target = tg
		}
		if hasEnabled {
			f.enabled = en
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// TestEjectMediaOnEjectedMediaSendsNothing: media already out is a
// success, and the POST the iLO 4 would answer 500 is never sent.
func TestEjectMediaOnEjectedMediaSendsNothing(t *testing.T) {
	f := &ilo4CleanupFake{inserted: false}
	b := f.serve(t)
	if err := b.EjectMedia(context.Background()); err != nil {
		t.Fatalf("EjectMedia on ejected media: %v", err)
	}
	if f.posts != 0 {
		t.Errorf("got %d eject POSTs, want none: the media was already out", f.posts)
	}
}

// TestEjectMediaAcceptsA500ThatEjected: the POST answers 500 but the media
// reads out afterwards -- the outcome the caller needs, so no error.
func TestEjectMediaAcceptsA500ThatEjected(t *testing.T) {
	f := &ilo4CleanupFake{inserted: true, ejectFails: true}
	b := f.serve(t)
	if err := b.EjectMedia(context.Background()); err != nil {
		t.Fatalf("EjectMedia: %v, want nil: Inserted reads false after the POST", err)
	}
	if f.posts != 1 {
		t.Errorf("got %d eject POSTs, want 1", f.posts)
	}
}

// TestEjectMediaReportsA500ThatLeftTheMediaIn: the second read is what
// decides, not the 500 alone; media still in keeps the error.
func TestEjectMediaReportsA500ThatLeftTheMediaIn(t *testing.T) {
	f := &ilo4CleanupFake{inserted: true, ejectNoop: true}
	b := f.serve(t)
	err := b.EjectMedia(context.Background())
	if err == nil {
		t.Fatal("want an error: the media is still inserted")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not carry the BMC's answer", err)
	}
}

// TestClearBootOverrideSendsTheTargetWithEnabled: an active override is
// cleared with Target None beside Enabled Disabled, which the iLO 4
// accepts, instead of Enabled alone, which it refuses with 400.
func TestClearBootOverrideSendsTheTargetWithEnabled(t *testing.T) {
	f := &ilo4CleanupFake{enabled: "Once", target: "Cd"}
	b := f.serve(t)
	if err := b.ClearBootOverride(context.Background()); err != nil {
		t.Fatalf("ClearBootOverride: %v", err)
	}
	if f.enabled != "Disabled" || f.target != "None" {
		t.Errorf("override = %s/%s after clearing, want Disabled/None", f.enabled, f.target)
	}
	if len(f.patches) != 1 {
		t.Fatalf("got %d PATCHes, want 1", len(f.patches))
	}
	boot, _ := f.patches[0]["Boot"].(map[string]any)
	if _, present := boot["BootSourceOverrideMode"]; present {
		t.Errorf("PATCH %v sets BootSourceOverrideMode, which ClearBootOverride must leave alone", f.patches[0])
	}
}

// TestClearBootOverrideWritesNothingWhenAlreadyDisabled: the usual iLO 4
// state at the end of an install (SetBootOnce sets no system override
// there); nothing is written.
func TestClearBootOverrideWritesNothingWhenAlreadyDisabled(t *testing.T) {
	f := &ilo4CleanupFake{enabled: "Disabled", target: "None"}
	b := f.serve(t)
	if err := b.ClearBootOverride(context.Background()); err != nil {
		t.Fatalf("ClearBootOverride: %v", err)
	}
	if len(f.patches) != 0 {
		t.Errorf("got %d PATCHes, want none: the override was already disabled", len(f.patches))
	}
}

// TestClearBootOverrideSurfacesAFailedRead: without the current state the
// call cannot tell whether an override is active; it says so rather than
// reporting a clean machine.
func TestClearBootOverrideSurfacesAFailedRead(t *testing.T) {
	b, _ := newRedfishBMCTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "iLO is busy", http.StatusServiceUnavailable)
	})
	err := b.ClearBootOverride(context.Background())
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want the 503 from reading the system", err)
	}
}
