package provision

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests drive SetBootOnce against a fake Redfish service that behaves
// like the schema it serves: a PATCH on the system resource naming a Boot
// property the stored Boot object does not carry is refused with 400, the
// way the real iLO 4's ComputerSystem.1.0.1 Boot schema
// (additionalProperties: false) refuses BootSourceOverrideMode. The iLO 4
// documents are the real captures under
// internal/redfish/testdata/ilo4-real (see PROVENANCE.md there), not
// hand-written approximations of them.

const (
	fakeSystemPath = "/redfish/v1/Systems/1/"
	fakeBIOSPath   = "/redfish/v1/systems/1/bios/"
	fakeVMPath     = "/redfish/v1/Managers/1/VirtualMedia/2/"
)

type recordedPatch struct {
	path string
	body map[string]any
}

type fakeRedfish struct {
	mu      sync.Mutex
	docs    map[string]map[string]any
	patches []recordedPatch
	// dropVMPatch answers a VirtualMedia PATCH 200 without applying it: a
	// BMC that accepts a write and does not keep it.
	dropVMPatch bool
}

func loadILO4Capture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "redfish", "testdata", "ilo4-real", name))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc
}

// newILO4Fake serves the real ML350 Gen9 captures: the system (Boot
// without BootSourceOverrideMode), its HP BIOS resource (BootMode: Uefi),
// and the virtual CD (Oem.Hp.BootOnNextServerReset: false).
func newILO4Fake(t *testing.T) *fakeRedfish {
	t.Helper()
	return &fakeRedfish{docs: map[string]map[string]any{
		fakeSystemPath: loadILO4Capture(t, "systems_1.json"),
		fakeBIOSPath:   loadILO4Capture(t, "systems_1_bios.json"),
		fakeVMPath:     loadILO4Capture(t, "managers_1_virtualmedia_2.json"),
	}}
}

// standardVirtualMedia is a VirtualMedia resource with no Oem.Hp: what a
// non-HP BMC serves.
func standardVirtualMedia() map[string]any {
	return map[string]any{"Inserted": true, "Image": "http://provisiond/iso/tok.iso"}
}

// newStandardFake is a recent Redfish service: its Boot object carries
// BootSourceOverrideMode, so the mode is set with the override itself.
func newStandardFake() *fakeRedfish {
	return &fakeRedfish{docs: map[string]map[string]any{
		fakeSystemPath: {"Boot": map[string]any{
			"BootSourceOverrideEnabled": "Disabled",
			"BootSourceOverrideTarget":  "None",
			"BootSourceOverrideMode":    "UEFI",
		}},
		fakeVMPath: standardVirtualMedia(),
	}}
}

func (f *fakeRedfish) serve(t *testing.T) *RedfishBMC {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return NewRedfishBMC(srv.URL, "admin", "s3cret", nil)
}

func (f *fakeRedfish) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	doc, ok := f.docs[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(doc)
	case http.MethodPatch:
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "malformed JSON", http.StatusBadRequest)
			return
		}
		f.patches = append(f.patches, recordedPatch{path: r.URL.Path, body: body})
		if msg := f.apply(r.URL.Path, doc, body); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// apply merges a PATCH into the stored document, refusing any property the
// document's schema does not have. It returns the refusal message, or "".
func (f *fakeRedfish) apply(path string, doc, body map[string]any) string {
	switch path {
	case fakeSystemPath:
		stored, _ := doc["Boot"].(map[string]any)
		patch, _ := body["Boot"].(map[string]any)
		for k := range body {
			if k != "Boot" {
				return "Base.0.10.PropertyNotWritable: " + k
			}
		}
		for k := range patch {
			if _, known := stored[k]; !known {
				return "Base.0.10.PropertyUnknown: Boot/" + k
			}
		}
		maps.Copy(stored, patch)
	case fakeVMPath:
		oem, _ := doc["Oem"].(map[string]any)
		hp, _ := oem["Hp"].(map[string]any)
		patchOem, _ := body["Oem"].(map[string]any)
		patchHp, _ := patchOem["Hp"].(map[string]any)
		if hp == nil || patchHp == nil {
			return "Base.0.10.PropertyUnknown: Oem"
		}
		v, ok := patchHp["BootOnNextServerReset"]
		if !ok || len(patchHp) != 1 || len(body) != 1 {
			return "Base.0.10.PropertyNotWritable"
		}
		if !f.dropVMPatch {
			hp["BootOnNextServerReset"] = v
		}
	default:
		return "Base.0.10.ActionNotSupported"
	}
	return ""
}

func (f *fakeRedfish) patchesTo(path string) []recordedPatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedPatch
	for _, p := range f.patches {
		if p.path == path {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeRedfish) allPatches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.patches)
}

// TestSetBootOnceSendsTheModeWhereTheBootSchemaHasIt: a recent Redfish
// service still receives the full override -- Once, the target, and the
// mode as asked, for both modes Install can pass.
func TestSetBootOnceSendsTheModeWhereTheBootSchemaHasIt(t *testing.T) {
	for _, mode := range []string{"UEFI", "Legacy"} {
		t.Run(mode, func(t *testing.T) {
			f := newStandardFake()
			b := f.serve(t)
			if err := b.SetBootOnce(context.Background(), "Cd", mode); err != nil {
				t.Fatal(err)
			}
			patches := f.patchesTo(fakeSystemPath)
			if len(patches) != 1 {
				t.Fatalf("got %d PATCHes on the system, want 1", len(patches))
			}
			boot, _ := patches[0].body["Boot"].(map[string]any)
			want := map[string]any{
				"BootSourceOverrideEnabled": "Once",
				"BootSourceOverrideTarget":  "Cd",
				"BootSourceOverrideMode":    mode,
			}
			for k, v := range want {
				if boot[k] != v {
					t.Errorf("Boot.%s = %v, want %v", k, boot[k], v)
				}
			}
			if f.allPatches() != 1 {
				t.Errorf("got %d PATCHes in all, want only the system override", f.allPatches())
			}
		})
	}
}

// oldSchemaFake is the iLO 4's system and BIOS with a virtual CD that has
// no Oem.Hp: it isolates the Boot-schema handling from the iLO 4 virtual
// media path.
func oldSchemaFake(t *testing.T) *fakeRedfish {
	t.Helper()
	f := newILO4Fake(t)
	f.docs[fakeVMPath] = standardVirtualMedia()
	return f
}

// TestSetBootOnceOmitsTheModeWhereTheBootSchemaLacksIt: the Boot object of
// ComputerSystem.1.0.1 has no BootSourceOverrideMode, and the fake refuses
// it as the real schema does. The override must go through without it.
func TestSetBootOnceOmitsTheModeWhereTheBootSchemaLacksIt(t *testing.T) {
	f := oldSchemaFake(t)
	b := f.serve(t)
	if err := b.SetBootOnce(context.Background(), "Cd", "UEFI"); err != nil {
		t.Fatal(err)
	}
	patches := f.patchesTo(fakeSystemPath)
	if len(patches) != 1 {
		t.Fatalf("got %d PATCHes on the system, want 1", len(patches))
	}
	boot, _ := patches[0].body["Boot"].(map[string]any)
	if _, present := boot["BootSourceOverrideMode"]; present {
		t.Errorf("Boot %v carries BootSourceOverrideMode, which this schema refuses", boot)
	}
	if boot["BootSourceOverrideEnabled"] != "Once" || boot["BootSourceOverrideTarget"] != "Cd" {
		t.Errorf("Boot = %v, want Once on Cd", boot)
	}
}

// TestSetBootOnceRefusesAModeTheMachineDoesNotBootIn: the mode cannot be
// set through this schema, so a request the BIOS contradicts is refused
// before anything is written -- never silently inherited.
func TestSetBootOnceRefusesAModeTheMachineDoesNotBootIn(t *testing.T) {
	f := oldSchemaFake(t)
	b := f.serve(t)
	err := b.SetBootOnce(context.Background(), "Cd", "Legacy")
	if err == nil {
		t.Fatal("want a refusal: the BIOS boots Uefi and Legacy was asked")
	}
	if !strings.Contains(err.Error(), "Uefi") {
		t.Errorf("error %q does not name the mode the machine is in", err)
	}
	if n := f.allPatches(); n != 0 {
		t.Errorf("got %d PATCHes, want none before the mode is confirmed", n)
	}
}

// TestSetBootOnceRefusesWhenTheModeCannotBeRead: no Boot property to set
// the mode and no BIOS to read it from means Install's explicit mode can be
// neither applied nor checked. Refused, with nothing written.
func TestSetBootOnceRefusesWhenTheModeCannotBeRead(t *testing.T) {
	cases := map[string]struct {
		breakIt func(f *fakeRedfish)
		wantErr string
	}{
		"bios unreachable": {
			func(f *fakeRedfish) { delete(f.docs, fakeBIOSPath) }, "404",
		},
		"bios without BootMode": {
			func(f *fakeRedfish) { delete(f.docs[fakeBIOSPath], "BootMode") }, "carries no BootMode",
		},
		"no bios link": {
			func(f *fakeRedfish) {
				oem, _ := f.docs[fakeSystemPath]["Oem"].(map[string]any)
				hp, _ := oem["Hp"].(map[string]any)
				delete(hp, "links")
			}, "no BIOS resource",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := oldSchemaFake(t)
			c.breakIt(f)
			b := f.serve(t)
			err := b.SetBootOnce(context.Background(), "Cd", "UEFI")
			if err == nil {
				t.Fatal("want a refusal when the boot mode can be neither set nor read")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not say why (want %q)", err, c.wantErr)
			}
			if n := f.allPatches(); n != 0 {
				t.Errorf("got %d PATCHes, want none", n)
			}
		})
	}
}

// TestSetBootOnceOnILO4BootsTheVirtualCDNotAnOverride: on the iLO 4 the
// one-shot boot is asked of the virtual CD itself
// (Oem.Hp.BootOnNextServerReset), and no Cd override is set on the system:
// the machine's UEFI boot list also holds a physical SATA DVD (CD.Emb.9.1),
// and nothing shows that "Cd" would pick the virtual drive over it.
func TestSetBootOnceOnILO4BootsTheVirtualCDNotAnOverride(t *testing.T) {
	f := newILO4Fake(t)
	b := f.serve(t)
	if err := b.SetBootOnce(context.Background(), "Cd", "UEFI"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.patchesTo(fakeSystemPath)); n != 0 {
		t.Errorf("got %d PATCHes on the system, want no Cd override on an iLO 4", n)
	}
	vm := f.patchesTo(fakeVMPath)
	if len(vm) != 1 {
		t.Fatalf("got %d PATCHes on the virtual CD, want 1", len(vm))
	}
	got, _ := json.Marshal(vm[0].body)
	if string(got) != `{"Oem":{"Hp":{"BootOnNextServerReset":true}}}` {
		t.Errorf("virtual CD PATCH = %s, want only Oem.Hp.BootOnNextServerReset: true", got)
	}
	hp := f.docs[fakeVMPath]["Oem"].(map[string]any)["Hp"].(map[string]any)
	if hp["BootOnNextServerReset"] != true {
		t.Errorf("the fake's virtual CD holds BootOnNextServerReset = %v, want true", hp["BootOnNextServerReset"])
	}
}

// TestSetBootOnceOnILO4RefusesAWrongModeBeforeWriting: the virtual media
// path sets no mode either, so it is confirmed against the BIOS first.
func TestSetBootOnceOnILO4RefusesAWrongModeBeforeWriting(t *testing.T) {
	f := newILO4Fake(t)
	b := f.serve(t)
	err := b.SetBootOnce(context.Background(), "Cd", "Legacy")
	if err == nil {
		t.Fatal("want a refusal: the BIOS boots Uefi and Legacy was asked")
	}
	if !strings.Contains(err.Error(), "Uefi") {
		t.Errorf("error %q does not name the mode the machine is in", err)
	}
	if n := f.allPatches(); n != 0 {
		t.Errorf("got %d PATCHes, want none", n)
	}
}

// TestSetBootOnceOnILO4FailsWhenTheFlagDoesNotStick: a 200 on the PATCH is
// not taken as the flag being set; it is read back, and a BMC that
// answered yes and kept nothing fails MediaAttached here rather than
// letting the reset boot whatever comes first.
func TestSetBootOnceOnILO4FailsWhenTheFlagDoesNotStick(t *testing.T) {
	f := newILO4Fake(t)
	f.dropVMPatch = true
	b := f.serve(t)
	err := b.SetBootOnce(context.Background(), "Cd", "UEFI")
	if err == nil {
		t.Fatal("want an error when BootOnNextServerReset reads back false")
	}
	if !strings.Contains(err.Error(), "BootOnNextServerReset") {
		t.Errorf("error %q does not name the flag that did not stick", err)
	}
}
