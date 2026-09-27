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
//   - GETs on the paths of readPaths, exactly as written (no parameter from
//     the cluster, the CR or the caller ever reaches a URL).
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

// Paths the client may GET, verbatim.
const (
	PathPools      = "/api/pool"
	PathOSDs       = "/api/osd"
	PathCrushRules = "/api/crush_rule"
	pathAuth       = "/api/auth"
)

var readPaths = []string{PathPools, PathOSDs, PathCrushRules}

// acceptV1 pins the dashboard API version the parsers are written against.
const acceptV1 = "application/vnd.ceph.api.v1.0+json"

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
}

// Client talks to one dashboard.
type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client

	mu    sync.Mutex
	token string
}

// New validates cfg and builds a client. It refuses anything but https
// with a CA to verify against.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("dashboard URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("dashboard URL %q is not https: the password would travel in clear; enable spec.dashboard.ssl on the CephCluster", cfg.BaseURL)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("dashboard URL %q carries a path; give scheme://host:port only", cfg.BaseURL)
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

func (c *Client) login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"username": c.cfg.Username, "password": c.cfg.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(pathAuth), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptV1)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dashboard login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
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

// get fetches one listed path, logging in first if needed and once more on
// a 401 (an expired token).
func (c *Client) get(ctx context.Context, path string, into any) error {
	if !slices.Contains(readPaths, path) {
		return fmt.Errorf("refusing GET %q: not on the dashboard read list", path)
	}
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		tok := c.token
		c.mu.Unlock()
		if tok == "" {
			if err := c.login(ctx); err != nil {
				return err
			}
			c.mu.Lock()
			tok = c.token
			c.mu.Unlock()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", acceptV1)
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

// Snapshot is one read of the three lists.
type Snapshot struct {
	Pools []Pool
	OSDs  []OSD
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

// Read fetches pools, OSDs and CRUSH rules. The dashboard names a pool's
// rule in /api/pool; an older one gives its id; either way it is rendered
// "<id> <name>" through /api/crush_rule.
func (c *Client) Read(ctx context.Context) (*Snapshot, error) {
	var pools []rawPool
	if err := c.get(ctx, PathPools, &pools); err != nil {
		return nil, err
	}
	var osds []rawOSD
	if err := c.get(ctx, PathOSDs, &osds); err != nil {
		return nil, err
	}
	var rules []rawRule
	if err := c.get(ctx, PathCrushRules, &rules); err != nil {
		return nil, err
	}
	names := map[int32]string{}
	for _, r := range rules {
		names[r.RuleID] = r.RuleName
	}
	out := &Snapshot{}
	for _, p := range pools {
		out.Pools = append(out.Pools, Pool{Name: p.PoolName, Size: p.Size, MinSize: p.MinSize, CrushRule: ruleName(p.CrushRule, names)})
	}
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
