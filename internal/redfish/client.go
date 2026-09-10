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

// Package redfish is a hand-written client for the handful of Redfish
// resources this operator needs from an HPE iLO4 baseboard management
// controller: the system document, chassis thermal/power telemetry, the
// manager (BMC) firmware version, the integrated management log, and the
// processor/memory/network component inventory. It is net/http and
// encoding/json only, by design: a general-purpose Redfish library buys
// breadth across vendors this lot doesn't need, at the cost of tolerance for
// the specific fields an iLO4 omits, which this package gets for free from
// pointer decoding instead.
//
// This package has no dependency on the Kubernetes API types — it is usable
// and testable standalone. Mapping a Snapshot onto a CRD's status is a
// caller's concern.
//
// client.go holds the transport layer (HTTP, auth, error mapping) and the
// public API. The resource-walking helpers that decode each Redfish document
// into this package's value types live in decode.go.
package redfish

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// errNotFound is returned by do/get when a resource answers 404. It is not
// exported: callers outside this package only ever see ErrAuth, ErrTLS,
// ErrUnsupported, or a plain wrapped error. Inside the package it lets Probe
// tell "this firmware doesn't expose this resource" apart from every other
// failure.
var errNotFound = errors.New("redfish: resource not found")

// Client is what the controller talks to a BMC through. Probe returns one
// complete snapshot; the other three methods are the write-side actions the
// operator needs (power control, log hygiene, physical identification).
type Client interface {
	Probe(ctx context.Context) (*Snapshot, error)
	Reset(ctx context.Context, resetType string) error
	ClearLog(ctx context.Context) error
	SetIndicatorLED(ctx context.Context, on bool) error
}

type client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// New builds a Client. tlsConfig controls certificate verification for the
// connection to the BMC; pass &tls.Config{InsecureSkipVerify: true} for a
// self-signed BMC certificate, or a config with a configured RootCAs to trust
// a specific CA. An iLO4 is slow to answer under load, so the client's
// timeout is a generous 20 seconds rather than Go's default of none.
func New(baseURL, username, password string, tlsConfig *tls.Config) Client {
	return &client{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		username: username,
		password: password,
		httpClient: &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}
}

// do issues one HTTP request against the BMC with Basic auth and maps
// transport/HTTP failures onto this package's sentinel errors. body, when
// non-nil, is marshalled as the JSON request body. out, when non-nil, is
// filled by decoding a successful response body.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("redfish: encode request for %s: %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+normalizePath(path), reader)
	if err != nil {
		return fmt.Errorf("redfish: build request for %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.username, c.password)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		var verifyErr *tls.CertificateVerificationError
		var authorityErr x509.UnknownAuthorityError
		if errors.As(err, &verifyErr) || errors.As(err, &authorityErr) {
			return fmt.Errorf("%s: %w", path, ErrTLS)
		}
		return fmt.Errorf("redfish: request %s: %w", path, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s: %w", path, ErrAuth)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s: %w", path, errNotFound)
	case resp.StatusCode >= 300:
		return fmt.Errorf("redfish: %s returned %s", path, resp.Status)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("redfish: decode %s: %w", path, err)
	}
	return nil
}

// normalizePath ensures path ends with a trailing slash before it is sent to
// the BMC. Finding 4: the captured iLO4 answers a path missing its trailing
// slash with an HTTP 308 rather than the resource (e.g. GET
// /redfish/v1/Systems/1 vs .../Systems/1/). Go's http.Client follows 308 for
// both GET and POST and preserves the body, so relying on that redirect
// already works — this removes the dependency rather than fixing a bug:
// every request this package issues should ask for the resource it means,
// not for a redirect to it, regardless of whether the path came from a
// hardcoded literal or was read back out of a Redfish "@odata.id" field that
// might not carry the trailing slash on some other firmware. A path
// carrying a query string is left alone: appending "/" before "?" would ask
// for a different, wrong resource.
func normalizePath(path string) string {
	if path == "" || strings.HasSuffix(path, "/") || strings.ContainsRune(path, '?') {
		return path
	}
	return path + "/"
}

func (c *client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *client) post(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPost, path, body, nil)
}

func (c *client) patch(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// Probe reads one complete snapshot of the machine. Only a failure to read
// the service root or the system document fails the call; every resource
// after that (chassis telemetry, manager firmware, the log, the component
// inventory) is read on a best-effort basis — a 404 leaves the corresponding
// part of the snapshot at its zero value, because that is what an iLO4
// firmware that doesn't expose the resource looks like.
//
// The service root itself gets a stricter check than "a 404 is tolerable":
// a 404 on /redfish/v1/, or a root that decodes but exposes no Systems
// collection at all, means whatever answered at spec.bmc.address is not a
// Redfish service — a web server, a switch, or simply the wrong IP. That is
// an ordinary registration mistake, not a transient firmware gap the way a
// missing Chassis sub-resource further down is, so it is reported as
// ErrUnsupported rather than falling through to the generic ProbeFailed a
// JSON-decode error against the wrong kind of body would otherwise produce.
func (c *client) Probe(ctx context.Context) (*Snapshot, error) {
	root, err := c.readServiceRoot(ctx)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("redfish: %s answered no service root at /redfish/v1/: %w", c.baseURL, ErrUnsupported)
		}
		return nil, err
	}
	if root.Systems.ODataID == "" {
		return nil, fmt.Errorf("redfish: %s's service root exposes no Systems collection, not a Redfish service: %w", c.baseURL, ErrUnsupported)
	}

	systemPath, err := c.firstMember(ctx, root.Systems.ODataID)
	if err != nil {
		return nil, fmt.Errorf("redfish: resolve system: %w", err)
	}
	if systemPath == "" {
		return nil, fmt.Errorf("redfish: no system members")
	}

	var sys computerSystemJSON
	if err := c.get(ctx, systemPath, &sys); err != nil {
		return nil, fmt.Errorf("redfish: read system document: %w", err)
	}

	snap := &Snapshot{
		PowerState:          sys.PowerState,
		PostState:           sys.Oem.Hp.PostState,
		AllowableResetTypes: sys.Actions.Reset.AllowableValues,
		IndicatorLED:        sys.IndicatorLED,
		Inventory: Inventory{
			Manufacturer:   sys.Manufacturer,
			Model:          sys.Model,
			SerialNumber:   sys.SerialNumber,
			BIOSVersion:    sys.BiosVersion,
			TotalMemoryGiB: sys.MemorySummary.TotalSystemMemoryGiB,
			// Drives is deliberately left empty. An iLO4 exposes physical
			// drives under HPE's OEM SmartStorage tree, not the standard
			// Storage/Drives collections the rest of this package walks —
			// guessing that OEM path without a real machine to check it
			// against is how a plausible-looking wrong implementation
			// ships. It gets filled in after first contact with hardware.
		},
		LogCounts: map[string]int{},
	}

	// Finding 1: the captured iLO4 replays cached Thermal/Power readings as
	// if they were live, whatever the machine's actual state — CPU1 reports
	// 40 C with Status.State "Enabled" whether the machine is off, mid-POST,
	// or finished, twenty minutes after being powered down in a room at
	// 20 C. Status.State and the reading itself cannot tell a measurement
	// from a memory (see Snapshot.SensorsTrustworthy and
	// testdata/ilo4-real/PROVENANCE.md), so trustworthiness is derived from
	// PowerState and the OEM PostState instead: powered-on is the necessary
	// condition, and PowerOff/InPost are the two states observed to still
	// serve stale data while powered on.
	//
	// An unrecognised PostState on a powered-on machine is treated as
	// trustworthy. The captured machine has no operating system installed —
	// POST stops at InPostDiscoveryComplete for lack of a boot device — so
	// the PostState a booted machine reports has never been seen. Refusing
	// every unknown value would make sensors permanently unavailable on the
	// first machine that actually boots, which is a worse failure mode than
	// occasionally trusting a state this package has not catalogued yet.
	snap.SensorsTrustworthy = snap.PowerState == "On" &&
		snap.PostState != "PowerOff" && snap.PostState != "InPost"

	if err := c.readManager(ctx, snap); err != nil {
		return nil, err
	}
	if err := c.readSensors(ctx, root.Chassis.ODataID, snap); err != nil {
		return nil, err
	}
	if err := c.readComponentInventory(ctx, systemPath, snap); err != nil {
		return nil, err
	}
	if sys.LogServices.ODataID != "" {
		if err := c.readLog(ctx, sys.LogServices.ODataID+"IML/Entries/", snap); err != nil {
			return nil, err
		}
	}

	return snap, nil
}

// Reset POSTs a ComputerSystem.Reset action to the target the system
// document advertises. If the document has no such action, the BMC doesn't
// support it and Reset returns ErrUnsupported rather than attempting a POST
// to a guessed URL.
//
// resetType is resolved against what this machine's
// Actions.#ComputerSystem.Reset actually lists in
// ResetType@Redfish.AllowableValues before it is sent — see resolveResetType
// for why GracefulShutdown is the one value this needs to do that for
// (Finding 2).
func (c *client) Reset(ctx context.Context, resetType string) error {
	systemPath, err := c.resolveSystemPath(ctx)
	if err != nil {
		return err
	}

	var sys computerSystemJSON
	if err := c.get(ctx, systemPath, &sys); err != nil {
		return fmt.Errorf("redfish: read system document: %w", err)
	}
	target := sys.Actions.Reset.Target
	if target == "" {
		return ErrUnsupported
	}

	resolved, err := resolveResetType(resetType, sys.Actions.Reset.AllowableValues)
	if err != nil {
		return err
	}

	body := struct {
		ResetType string `json:"ResetType"`
	}{ResetType: resolved}
	return c.post(ctx, target, body)
}

// resolveResetType maps a caller's requested ResetType onto one the machine
// actually accepts. Only GracefulShutdown needs mapping: the CRD enum keeps
// that name because it is what a person means and the enum is a frozen API,
// but it is not itself a Redfish ResetType on the captured hardware — an
// iLO4's Actions#ComputerSystem.Reset lists On, ForceOff, ForceRestart, Nmi
// and PushPowerButton and nothing else (Finding 2). PushPowerButton is the
// ACPI power-button request an installed operating system acts on, which is
// what "graceful" means in practice, so it is the substitute when
// GracefulShutdown itself is not listed. ForceOff is never substituted
// automatically: it is a hard cut, not a graceful one, and choosing it would
// silently turn a request that asked to be graceful into one that was not,
// with no way for the caller to know that happened.
//
// Every other requested type passes through unchanged — this package does
// not second-guess On/ForceOff/ForceRestart/Nmi/PushPowerButton against the
// allow list, only the one value this lot found is never actually offered.
func resolveResetType(requested string, allowable []string) (string, error) {
	if requested != "GracefulShutdown" {
		return requested, nil
	}
	if containsString(allowable, "GracefulShutdown") {
		return "GracefulShutdown", nil
	}
	if containsString(allowable, "PushPowerButton") {
		return "PushPowerButton", nil
	}
	return "", ErrUnsupported
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// ClearLog POSTs a LogService.ClearLog action to the integrated management
// log for the machine's system.
func (c *client) ClearLog(ctx context.Context) error {
	systemPath, err := c.resolveSystemPath(ctx)
	if err != nil {
		return err
	}
	id := lastPathSegment(systemPath)
	target := fmt.Sprintf("/redfish/v1/Systems/%s/LogServices/IML/Actions/LogService.ClearLog/", id)

	body := struct {
		Action string `json:"Action"`
	}{Action: "LogService.ClearLog"}
	return c.post(ctx, target, body)
}

// SetIndicatorLED PATCHes the system document's IndicatorLED field.
func (c *client) SetIndicatorLED(ctx context.Context, on bool) error {
	systemPath, err := c.resolveSystemPath(ctx)
	if err != nil {
		return err
	}

	state := "Off"
	if on {
		state = "Lit"
	}
	body := struct {
		IndicatorLED string `json:"IndicatorLED"`
	}{IndicatorLED: state}
	return c.patch(ctx, systemPath, body)
}
