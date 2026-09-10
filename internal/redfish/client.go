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

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
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
func (c *client) Probe(ctx context.Context) (*Snapshot, error) {
	root, err := c.readServiceRoot(ctx)
	if err != nil {
		return nil, err
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
		PowerState:   sys.PowerState,
		IndicatorLED: sys.IndicatorLED,
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

	body := struct {
		ResetType string `json:"ResetType"`
	}{ResetType: resetType}
	return c.post(ctx, target, body)
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
