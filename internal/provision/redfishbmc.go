package provision

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gothshoot/frame/internal/redfish"
)

// RedfishBMC adapts lot 1's redfish.Client to the BMC interface Install
// drives, and supplies the calls that client's public interface has no
// reason to carry: Serial, Reset, ClearLog and SetIndicatorLED exist because
// FrameMachine -- redfish.Client's only consumer before this lot -- polls
// sensors and drives power, never virtual media or a boot override. Those
// two are new here.
//
// It lives in internal/provision, not beside the FrameInstall controller,
// for the reason install.go's package comment states: Task 10's frame
// bootstrap command has no cluster to run inside and cannot import a
// package that imports Kubernetes, and internal/redfish already carries no
// such import. internal/provision/imports_test.go covers this file the same
// way it covers every other one here.
//
// Unlike Reset/ClearLog/SetIndicatorLED/Probe, which lot 1 captured against
// real ML350 Gen9 hardware, InsertMedia/MediaInserted/EjectMedia/
// SetBootOnce/ClearBootOverride below are not yet verified against that
// machine's firmware -- they are modelled on HPE's published iLO4 RESTful
// API for the VirtualMedia OEM actions and the standard Redfish
// BootSourceOverride* properties the design (docs/superpowers/specs/2026-09-
// 11-provisioning-debian-design.md §5) names by field. Proving them is one
// of the three things this lot's plan states cannot be proven without
// hardware (§11): that the image boots.
type RedfishBMC struct {
	client   redfish.Client
	baseURL  string
	username string
	password string
	http     *http.Client
}

// NewRedfishBMC builds a RedfishBMC against the same three coordinates
// redfish.New takes: baseURL, credentials, and a TLS config. It needs them
// directly, rather than wrapping an already-built redfish.Client, because
// the virtual-media and boot-override calls below issue their own HTTP
// requests -- redfish.Client's interface has no method that would let this
// package reach into a *client built by redfish.New for them.
func NewRedfishBMC(baseURL, username, password string, tlsConfig *tls.Config) *RedfishBMC {
	return &RedfishBMC{
		client:   redfish.New(baseURL, username, password, tlsConfig),
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		username: username,
		password: password,
		http: &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}
}

// Serial reads Systems/1.SerialNumber through a full Probe. Install compares
// it against Options.ConfirmSerial before anything is built or attached --
// the destructive guard's second layer (design §8) -- so a probe failure
// here must fail that comparison outright rather than report an empty
// string that could compare equal to another empty confirmation; install.go
// already refuses that shape on both sides (an empty confirmed serial and an
// empty reported one), so returning the error unchanged is enough.
func (b *RedfishBMC) Serial(ctx context.Context) (string, error) {
	snap, err := b.client.Probe(ctx)
	if err != nil {
		return "", fmt.Errorf("redfish: reading serial: %w", err)
	}
	return snap.Inventory.SerialNumber, nil
}

// Reset delegates to lot 1's client, which already resolves a requested
// ResetType against this machine's own Actions#ComputerSystem.Reset
// AllowableValues (internal/redfish/client.go's resolveResetType) --
// GracefulShutdown becomes PushPowerButton there, because the captured iLO4
// does not list GracefulShutdown at all (Finding 2). install.go always
// requests ForceRestart, deliberately: the machine at that point in the
// sequence has nothing left to lose.
func (b *RedfishBMC) Reset(ctx context.Context, resetType string) error {
	return b.client.Reset(ctx, resetType)
}

// virtualMediaPath is VM2, the CD/DVD device. VM1 is Floppy/USBStick, and an
// installer ISO belongs on the CD device -- the remastered image
// (internal/provision/iso.go) is a CD image and boots from there.
const virtualMediaPath = "/redfish/v1/Managers/1/VirtualMedia/2/"

// InsertMedia mounts url as this machine's virtual CD. An iLO4 exposes
// virtual media through an HP OEM action rather than the standard Redfish
// VirtualMedia.InsertMedia action --
// Oem.Hp.Actions#HpiLOVirtualMedia.InsertVirtualMedia, POSTed with
// {"Image": url} -- so this cannot go through redfish.Client's public
// Probe/Reset/ClearLog/SetIndicatorLED surface, which lot 1 never exported
// because FrameMachine never needed a write here.
//
// A successful response here is not the same as media actually being
// attached: install.go's MediaAttached phase re-reads MediaInserted after
// this call returns, because InsertVirtualMedia answering 200 has been seen
// to not mean the media is there.
func (b *RedfishBMC) InsertMedia(ctx context.Context, url string) error {
	body := struct {
		Image string `json:"Image"`
	}{Image: url}
	return b.do(ctx, http.MethodPost, virtualMediaPath+"Actions/Oem/Hp/HpiLOVirtualMedia.InsertVirtualMedia/", body, nil)
}

// MediaInserted re-reads the VirtualMedia resource's own Inserted field --
// the standard Redfish VirtualMedia schema property -- which is what
// actually answers whether the media InsertMedia asked for is attached.
func (b *RedfishBMC) MediaInserted(ctx context.Context) (bool, error) {
	var vm struct {
		Inserted bool `json:"Inserted"`
	}
	if err := b.do(ctx, http.MethodGet, virtualMediaPath, nil, &vm); err != nil {
		return false, err
	}
	return vm.Inserted, nil
}

// EjectMedia unmounts the virtual CD through the matching HP OEM action.
// install.go's cleanup runs this on every exit once the machine has been
// touched, successful or not: a machine left with media attached reboots
// into the installer on its next power cycle (design §10).
func (b *RedfishBMC) EjectMedia(ctx context.Context) error {
	// The action takes no parameters; an empty JSON object, not a nil body,
	// so Content-Type is still set the same way every other write here sets
	// it -- an OEM action answering differently to a request with no body at
	// all is not a risk worth taking with no hardware to check it against.
	return b.do(ctx, http.MethodPost, virtualMediaPath+"Actions/Oem/Hp/HpiLOVirtualMedia.EjectVirtualMedia/", struct{}{}, nil)
}

// systemPath is the one system this package drives; iLO 4 answers 308 on
// the same path without its trailing slash.
const systemPath = "/redfish/v1/Systems/1/"

// SetBootOnce sets a one-shot boot override to target ("Cd") in mode
// ("UEFI" or "Legacy" -- see Options.BootMode's comment in types.go: never
// inherited from the machine).
//
// The mode is sent as BootSourceOverrideMode only when the system's own
// Boot object carries that property. The iLO 4 of the ML350 Gen9 serves
// ComputerSystem.1.0.1, whose Boot schema has no such property and forbids
// additional ones (internal/redfish/testdata/ilo4-real/systems_1.json):
// sending it there makes the whole PATCH fail. Where the mode cannot be
// set, it is checked instead -- read from the HP BIOS resource
// (Oem.Hp.links.BIOS, BootMode "Uefi" or "LegacyBios") -- and the request
// is refused before anything is written if the machine boots in another
// mode, or if that mode cannot be read at all: going ahead would be
// inheriting the machine's mode, which Options.BootMode exists to prevent.
//
// On an iLO 4 -- recognised by an Oem.Hp object on the virtual CD resource,
// not by a firmware version string -- a "Cd" boot is not asked of the
// system at all. The virtual CD's own Oem.Hp.BootOnNextServerReset is set
// instead, and read back. The reason is the machine's UEFI boot list: it
// holds a physical SATA DVD (CD.Emb.9.1 in systems_1.json), and nothing
// shows that a "Cd" override would boot the virtual drive rather than that
// empty tray. BootOnNextServerReset names the virtual drive itself. No
// override is set beside it, so there is no second instruction that could
// win at the reset and point at the wrong drive. ClearBootOverride still
// disables the system override on the way out, which is harmless when none
// was set; EjectMedia removes the medium the flag points at.
func (b *RedfishBMC) SetBootOnce(ctx context.Context, target, mode string) error {
	sys, err := b.readSystem(ctx)
	if err != nil {
		return err
	}
	if target == "Cd" {
		hp, err := b.virtualMediaIsILO4(ctx)
		if err != nil {
			return err
		}
		if hp {
			if err := b.checkBootMode(ctx, sys, mode); err != nil {
				return err
			}
			return b.bootVirtualCDOnce(ctx)
		}
	}
	if sys.Boot == nil {
		return fmt.Errorf("redfish: %s exposes no Boot object to set an override on", systemPath)
	}
	_, modeSettable := sys.Boot["BootSourceOverrideMode"]

	boot := map[string]string{
		"BootSourceOverrideEnabled": "Once",
		"BootSourceOverrideTarget":  target,
	}
	if modeSettable {
		boot["BootSourceOverrideMode"] = mode
	} else if err := b.checkBootMode(ctx, sys, mode); err != nil {
		return err
	}
	return b.do(ctx, http.MethodPatch, systemPath, map[string]any{"Boot": boot}, nil)
}

// systemDoc is the part of the system resource SetBootOnce reads: the Boot
// object's keys (their presence is what matters, not only their values)
// and the HP link to the BIOS settings.
type systemDoc struct {
	Boot map[string]json.RawMessage `json:"Boot"`
	Oem  struct {
		Hp struct {
			Links struct {
				BIOS struct {
					Href string `json:"href"`
				} `json:"BIOS"`
			} `json:"links"`
		} `json:"Hp"`
	} `json:"Oem"`
}

func (b *RedfishBMC) readSystem(ctx context.Context) (systemDoc, error) {
	var sys systemDoc
	err := b.do(ctx, http.MethodGet, systemPath, nil, &sys)
	return sys, err
}

// virtualMediaIsILO4 reports whether the virtual CD carries an Oem.Hp
// object, which is where an iLO 4 keeps BootOnNextServerReset.
func (b *RedfishBMC) virtualMediaIsILO4(ctx context.Context) (bool, error) {
	var vm struct {
		Oem struct {
			Hp json.RawMessage `json:"Hp"`
		} `json:"Oem"`
	}
	if err := b.do(ctx, http.MethodGet, virtualMediaPath, nil, &vm); err != nil {
		return false, err
	}
	hp := bytes.TrimSpace(vm.Oem.Hp)
	return len(hp) > 0 && !bytes.Equal(hp, []byte("null")), nil
}

// bootVirtualCDOnce sets the iLO 4's Oem.Hp.BootOnNextServerReset on the
// virtual CD and reads it back: a 200 on the PATCH is not taken as the
// flag being set, the same way InsertMedia's 200 is not taken as media
// being attached.
func (b *RedfishBMC) bootVirtualCDOnce(ctx context.Context) error {
	body := map[string]any{"Oem": map[string]any{"Hp": map[string]any{"BootOnNextServerReset": true}}}
	if err := b.do(ctx, http.MethodPatch, virtualMediaPath, body, nil); err != nil {
		return err
	}
	var vm struct {
		Oem struct {
			Hp struct {
				BootOnNextServerReset bool `json:"BootOnNextServerReset"`
			} `json:"Hp"`
		} `json:"Oem"`
	}
	if err := b.do(ctx, http.MethodGet, virtualMediaPath, nil, &vm); err != nil {
		return fmt.Errorf("redfish: reading back BootOnNextServerReset: %w", err)
	}
	if !vm.Oem.Hp.BootOnNextServerReset {
		return fmt.Errorf("redfish: the BMC accepted Oem.Hp.BootOnNextServerReset on %s but reads it back false", virtualMediaPath)
	}
	return nil
}

// biosBootModes maps Options.BootMode to the HP BIOS schema's BootMode
// values (HpBios.1.2.0, captured as systems_1_bios.json).
var biosBootModes = map[string]string{
	bootUEFI:   "Uefi",
	bootLegacy: "LegacyBios",
}

// checkBootMode confirms the machine already boots in mode, for a BMC that
// offers no way to set it with the override. What it verifies is the BIOS
// setting the next boot will use; it cannot verify pending BIOS changes
// that would apply at that same reset (the HP BIOS keeps those in a
// separate Settings resource, not read here).
func (b *RedfishBMC) checkBootMode(ctx context.Context, sys systemDoc, mode string) error {
	want, ok := biosBootModes[mode]
	if !ok {
		return fmt.Errorf("redfish: boot mode %q: must be %q or %q", mode, bootUEFI, bootLegacy)
	}
	href := sys.Oem.Hp.Links.BIOS.Href
	if href == "" {
		return fmt.Errorf("redfish: this BMC cannot set the boot mode with the override (no BootSourceOverrideMode) and offers no BIOS resource to confirm it is %s; refusing rather than inheriting the machine's mode", mode)
	}
	var bios struct {
		BootMode string `json:"BootMode"`
	}
	if err := b.do(ctx, http.MethodGet, href, nil, &bios); err != nil {
		return fmt.Errorf("redfish: reading the boot mode to confirm it is %s: %w", mode, err)
	}
	if bios.BootMode == "" {
		return fmt.Errorf("redfish: %s carries no BootMode; cannot confirm the machine boots %s, refusing rather than inheriting its mode", href, mode)
	}
	if !strings.EqualFold(bios.BootMode, want) {
		return fmt.Errorf("redfish: the machine boots %s but %s was asked, and this BMC cannot change the mode through a boot override; change it in the BIOS first", bios.BootMode, mode)
	}
	return nil
}

// ClearBootOverride disables the boot override this installation set.
// install.go calls this on leaving Installing successfully and on entering
// Failed, so a machine that failed mid-install does not boot back into the
// installer on its own next restart. SetBootOnce already made the override
// one-shot; this is the second belt, not the only one (design §10).
func (b *RedfishBMC) ClearBootOverride(ctx context.Context) error {
	var body struct {
		Boot struct {
			BootSourceOverrideEnabled string `json:"BootSourceOverrideEnabled"`
		} `json:"Boot"`
	}
	body.Boot.BootSourceOverrideEnabled = "Disabled"
	return b.do(ctx, http.MethodPatch, systemPath, body, nil)
}

// do issues one authenticated HTTP request against the BMC. It deliberately
// does not reuse internal/redfish's client.do: that method is unexported,
// and duplicating this much smaller version here is the cost of the two
// packages' write surfaces staying disjoint (Probe/Reset/ClearLog/
// SetIndicatorLED through redfish.Client, everything else through this
// file) rather than punching a hole in redfish.Client's interface for five
// methods only this package calls.
func (b *RedfishBMC) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("redfish: encode request for %s: %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("redfish: build request for %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(b.username, b.password)

	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("redfish: request %s: %w", path, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("redfish: %s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("redfish: decode %s: %w", path, err)
	}
	return nil
}
