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

// Package cephmgr reads what Prometheus does not export — each pool's
// min_size and CRUSH rule, each OSD's CRUSH weight — from the Ceph
// dashboard's REST API (the mgr `dashboard` module; `restful` is gone from
// current Ceph releases), read-only.
//
// What it may send is fixed here and nowhere else:
//
//   - one POST, to /api/auth, to exchange the dedicated account's password
//     for a token — the dashboard has no other way to authenticate;
//   - GETs on the endpoints of readEndpoints, path, query and API version
//     exactly as written (no parameter from the cluster, the CR or the
//     caller ever reaches a URL).
//
// The account is meant to hold a custom role with read on the `pool` and
// `osd` scopes only (see docs/storage-mgr-account.md); a GET outside those
// scopes would be refused by the dashboard anyway, and the list here makes
// sure none is attempted.
//
// Transport: HTTPS only, the server certificate verified against the CA the
// Secret provides (the dashboard's certificate, pinned), with an explicit
// server name. A plain-HTTP URL is refused, not downgraded to.
package cephmgr

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
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Paths the client may GET.
const (
	PathPools      = "/api/pool"
	PathOSDs       = "/api/osd"
	PathCrushRules = "/api/crush_rule"
	pathAuth       = "/api/auth"
)

// endpoint is one GET the client may send: path, fixed query, and the API
// version the dashboard declares for it. The dashboard answers 415 when the
// Accept header's major version differs from the endpoint's, so each
// version is the one declared in the Ceph v20.2.2 source
// (src/pybind/mgr/dashboard/controllers/):
//
//   - pool.py       Pool.list        — default version, 1.0;
//   - osd.py        Osd.list         — MethodMap(version=APIVersion(1, 1)),
//     paginated, `limit: int = 10` by default: `limit=-1` asks for all;
//   - crush_rule.py CrushRule.list   — MethodMap(version=APIVersion(2, 0)).
type endpoint struct {
	Path    string
	Query   string
	Version string // "major.minor"
}

var readEndpoints = []endpoint{
	{Path: PathPools, Version: "1.0"},
	{Path: PathOSDs, Query: "limit=-1", Version: "1.1"},
	{Path: PathCrushRules, Version: "2.0"},
}

// authVersion is /api/auth's (auth.py: default version, 1.0).
const authVersion = "1.0"

func acceptFor(version string) string {
	return "application/vnd.ceph.api.v" + version + "+json"
}

// ErrAuthRefused is a refused login: a wrong, disabled or locked account.
// Ceph v20.2.2 answers a refused login with HTTP 400 and the body
// {"code": "invalid_credentials", "component": "auth", ...}
// (dashboard/controllers/auth.py, exceptions.py InvalidCredentialsError);
// a 401 on /api/auth is taken the same way. Ceph counts failed logins per
// user and only a successful login resets the count, so after this error
// no login is attempted again with the same account (see Reader).
var ErrAuthRefused = errors.New("dashboard login refused")

// maxBody bounds a response read: pool and OSD lists of a large cluster
// are well under this, and an endless body must not exhaust the manager.
const maxBody = 8 << 20

// Config is what the Secret provides.
type Config struct {
	BaseURL    string // https://host:port
	Username   string
	Password   string
	CAPEM      []byte // required: the dashboard's certificate or its CA
	ServerName string // name to verify the certificate against
	Timeout    time.Duration
	// Version identifies the Secret revision (its resourceVersion): a
	// changed account ends an authentication backoff at once.
	Version string
}

// Client talks to one dashboard.
type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client

	mu    sync.Mutex
	token string
	// loginFailed stops a pass at its first failed login: no GET of the
	// same pass logs in again (each attempt counts against the account).
	loginFailed error
	// gate is written before, and told after, every login (reader.go).
	gate LoginGate
	// gateNote says what the gate could not record, for the status.
	gateNote string
}

// New validates cfg and builds a client. It refuses anything but https
// with a CA to verify against.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		// url.Parse's own error quotes the whole string: not reproduced.
		return nil, errors.New("dashboard URL: not a valid URL")
	}
	// Error messages end up in status.message: they name scheme://host
	// only, never a userinfo, path, query or fragment the Secret may hold.
	shown := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	if u.Scheme != "https" {
		return nil, fmt.Errorf("dashboard URL %s is not https: the password would travel in clear; enable spec.dashboard.ssl on the CephCluster", shown)
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("dashboard URL %s: give scheme://host:port only (no credentials, path, query or fragment)", shown)
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("dashboard account: username and password are required")
	}
	pool := x509.NewCertPool()
	if len(cfg.CAPEM) == 0 || !pool.AppendCertsFromPEM(cfg.CAPEM) {
		return nil, errors.New("dashboard CA: no PEM certificate given; the server certificate cannot be verified without one")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: cfg.ServerName, MinVersion: tls.VersionTLS12},
		Proxy:           nil,
	}
	return &Client{
		cfg:  cfg,
		base: &url.URL{Scheme: u.Scheme, Host: u.Host},
		http: &http.Client{
			Transport: tr,
			Timeout:   cfg.Timeout,
			// A redirect would send the token elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) url(path string) string {
	u := *c.base
	u.Path = path
	return u.String()
}

// GateError wraps an error of the LoginGate: no login was sent, and the
// pass stops.
type GateError struct{ Err error }

func (e *GateError) Error() string { return "no login: " + e.Err.Error() }
func (e *GateError) Unwrap() error { return e.Err }

// login writes the intention to the gate first (no gate: tests only), then
// logs in, then records the outcome: success clears the intention, a
// refusal moves it to refused, anything else (network, 5xx, a bad answer)
// leaves it pending — its outcome is unknown, so the account stays
// blocked until an operator clears it.
func (c *Client) login(ctx context.Context) error {
	if c.gate != nil {
		if err := c.gate.Begin(ctx, c.cfg); err != nil {
			return &GateError{Err: err}
		}
	}
	err := c.doLogin(ctx)
	if c.gate != nil {
		switch {
		case err == nil:
			if gerr := c.gate.Succeeded(ctx, c.cfg); gerr != nil {
				c.mu.Lock()
				c.gateNote = "login succeeded but its intention could not be cleared: the next login after a restart is blocked (" + gerr.Error() + ")"
				c.mu.Unlock()
			}
		case errors.Is(err, ErrAuthRefused):
			if gerr := c.gate.Refused(ctx, c.cfg); gerr != nil {
				c.mu.Lock()
				c.gateNote = "refusal not moved to refused, still pending (blocked either way): " + gerr.Error()
				c.mu.Unlock()
			}
		}
	}
	return err
}

func (c *Client) doLogin(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"username": c.cfg.Username, "password": c.cfg.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(pathAuth), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptFor(authVersion))
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dashboard login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrAuthRefused
	}
	if resp.StatusCode == http.StatusBadRequest {
		var e struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e) == nil && e.Code == "invalid_credentials" {
			return ErrAuthRefused
		}
		return errors.New("dashboard login: HTTP 400")
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// A 3xx lands here too: redirects are never followed (the POST
		// carries the password).
		return fmt.Errorf("dashboard login: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&out); err != nil || out.Token == "" {
		return errors.New("dashboard login: no token in the answer")
	}
	c.mu.Lock()
	c.token = out.Token
	c.mu.Unlock()
	return nil
}

// get fetches one listed endpoint, logging in first if needed and once more
// on a 401 (an expired token).
func (c *Client) get(ctx context.Context, path string, into any) error {
	i := slices.IndexFunc(readEndpoints, func(e endpoint) bool { return e.Path == path })
	if i < 0 {
		return fmt.Errorf("refusing GET %q: not on the dashboard read list", path)
	}
	ep := readEndpoints[i]
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		tok := c.token
		c.mu.Unlock()
		if tok == "" {
			c.mu.Lock()
			failed := c.loginFailed
			c.mu.Unlock()
			if failed != nil {
				return failed
			}
			if err := c.login(ctx); err != nil {
				c.mu.Lock()
				c.loginFailed = err
				c.mu.Unlock()
				return err
			}
			c.mu.Lock()
			tok = c.token
			c.mu.Unlock()
		}
		u := *c.base
		u.Path, u.RawQuery = ep.Path, ep.Query
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", acceptFor(ep.Version))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("GET %s: %w", path, err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			// An expired token: forget it, log in again once.
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		if err != nil {
			return fmt.Errorf("GET %s: %w", path, err)
		}
		if err := json.Unmarshal(data, into); err != nil {
			return fmt.Errorf("GET %s: %w", path, err)
		}
		return nil
	}
	return fmt.Errorf("GET %s: still unauthorized after a fresh login", path)
}

// Pool is one pool's placement settings.
type Pool struct {
	Name      string
	Size      int32
	MinSize   int32
	CrushRule string
}

// OSD is one OSD's CRUSH placement weight.
type OSD struct {
	ID          int32
	CrushWeight float64
	DeviceClass string
}

// Snapshot is one read of the three lists. A list whose GET failed is nil
// and its error is in Errors: one endpoint failing never discards the
// others.
type Snapshot struct {
	Pools   []Pool
	OSDs    []OSD
	Errors  map[string]error // path → why it could not be read
	Fetched map[string]bool  // paths read in this pass
}

type rawPool struct {
	PoolName  string          `json:"pool_name"`
	Size      int32           `json:"size"`
	MinSize   int32           `json:"min_size"`
	CrushRule json.RawMessage `json:"crush_rule"`
}

type rawOSD struct {
	ID   int32 `json:"id"`
	OSD  int32 `json:"osd"`
	Tree struct {
		CrushWeight *float64 `json:"crush_weight"`
		DeviceClass string   `json:"device_class"`
	} `json:"tree"`
}

type rawRule struct {
	RuleID   int32  `json:"rule_id"`
	RuleName string `json:"rule_name"`
}

// Read fetches pools, OSDs and CRUSH rules, each on its own: a failure is
// recorded in Snapshot.Errors and the other lists are still returned. The
// error return is reserved for a failed login (ErrAuthRefused when the
// account is refused), which makes every GET pointless. Pool rules are rendered "<id> <name>" when the
// rule list was read, by name alone otherwise.
func (c *Client) Read(ctx context.Context) (*Snapshot, error) {
	// Log in once up front: a login that fails fails every GET the same
	// way, and is reported as such rather than three times.
	c.mu.Lock()
	c.loginFailed = nil // a new pass
	tok := c.token
	c.mu.Unlock()
	if tok == "" {
		if err := c.login(ctx); err != nil {
			return nil, err
		}
	}
	out := &Snapshot{Errors: map[string]error{}, Fetched: map[string]bool{}}
	var pools []rawPool
	if err := c.get(ctx, PathPools, &pools); err != nil {
		var gerr *GateError
		if errors.Is(err, ErrAuthRefused) || errors.As(err, &gerr) {
			return nil, err
		}
		out.Errors[PathPools] = err
		pools = nil
	} else {
		out.Fetched[PathPools] = true
	}
	var osds []rawOSD
	if err := c.get(ctx, PathOSDs, &osds); err != nil {
		var gerr *GateError
		if errors.Is(err, ErrAuthRefused) || errors.As(err, &gerr) {
			return nil, err
		}
		out.Errors[PathOSDs] = err
		osds = nil
	} else {
		out.Fetched[PathOSDs] = true
	}
	var rules []rawRule
	if err := c.get(ctx, PathCrushRules, &rules); err != nil {
		var gerr *GateError
		if errors.Is(err, ErrAuthRefused) || errors.As(err, &gerr) {
			return nil, err
		}
		out.Errors[PathCrushRules] = err
		rules = nil
	} else {
		out.Fetched[PathCrushRules] = true
	}
	names := map[int32]string{}
	for _, r := range rules {
		names[r.RuleID] = r.RuleName
	}
	if _, failed := out.Errors[PathPools]; !failed {
		out.Pools = []Pool{}
		for _, p := range pools {
			out.Pools = append(out.Pools, Pool{Name: p.PoolName, Size: p.Size, MinSize: p.MinSize, CrushRule: ruleName(p.CrushRule, names)})
		}
	}
	if _, failed := out.Errors[PathOSDs]; !failed {
		out.OSDs = []OSD{}
		for _, o := range osds {
			if o.Tree.CrushWeight == nil {
				continue // not in the CRUSH map: no weight to report, rather than 0
			}
			id := o.OSD
			if id == 0 {
				id = o.ID
			}
			out.OSDs = append(out.OSDs, OSD{ID: id, CrushWeight: *o.Tree.CrushWeight, DeviceClass: o.Tree.DeviceClass})
		}
	}
	return out, nil
}

// ruleName renders a pool's rule as "<id> <name>" whichever of the two the
// dashboard gave.
func ruleName(raw json.RawMessage, names map[int32]string) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		for id, n := range names {
			if n == s {
				return fmt.Sprintf("%d %s", id, n)
			}
		}
		return s
	}
	var id int32
	if json.Unmarshal(raw, &id) == nil {
		if n, ok := names[id]; ok {
			return fmt.Sprintf("%d %s", id, n)
		}
		return fmt.Sprint(id)
	}
	return strings.TrimSpace(string(raw))
}
