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

package cephmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDashboard serves the fixtures (anonymised, in the shape of the Ceph
// dashboard API v1.0) over TLS and records every request.
type fakeDashboard struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []string
	expire   int // answer 401 to the next n authenticated GETs
	tokens   int
}

func newFake(t *testing.T) *fakeDashboard {
	f := &fakeDashboard{t: t}
	files := map[string]string{PathPools: "pools.json", PathOSDs: "osds.json", PathCrushRules: "crush_rules.json"}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == pathAuth {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != "frame-storage-reader" || body["password"] != "s3cret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.mu.Lock()
			f.tokens++
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"tok","username":"frame-storage-reader","permissions":{"pool":["read"],"osd":["read"]}}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		if f.expire > 0 {
			f.expire--
			f.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Unlock()
		name, ok := files[r.URL.Path]
		if r.Method != http.MethodGet || !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDashboard) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw})
}

func (f *fakeDashboard) cfg() Config {
	return Config{BaseURL: f.srv.URL, Username: "frame-storage-reader", Password: "s3cret", CAPEM: f.caPEM(), ServerName: "example.com"}
}

func TestReadPoolsOSDsAndRules(t *testing.T) {
	f := newFake(t)
	c, err := New(f.cfg())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pools) != 10 {
		t.Fatalf("pools = %d", len(snap.Pools))
	}
	p := snap.Pools[1]
	if p.Name != "replicapool" || p.Size != 4 || p.MinSize != 2 || p.CrushRule != "20 hdd2_ssd2" {
		t.Errorf("replicapool = %+v", p)
	}
	if len(snap.OSDs) != 7 {
		t.Fatalf("OSDs with a CRUSH weight = %d, want 7 (osd.7 has none and must be absent, not 0)", len(snap.OSDs))
	}
	if snap.OSDs[0].CrushWeight != 2.0 || snap.OSDs[3].CrushWeight < 1.089 || snap.OSDs[3].DeviceClass != "hdd" {
		t.Errorf("osds = %+v", snap.OSDs)
	}
}

// Everything the client sends: one POST to /api/auth, then GETs on the
// listed paths only.
func TestOnlyTheAuthPostAndListedGETsAreSent(t *testing.T) {
	f := newFake(t)
	c, _ := New(f.cfg())
	if _, err := c.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /api/auth", "GET /api/pool", "GET /api/osd", "GET /api/crush_rule"}
	if !slices.Equal(f.requests, want) {
		t.Fatalf("requests %q, want %q", f.requests, want)
	}
	if err := c.get(context.Background(), "/api/osd/0/destroy", &struct{}{}); err == nil {
		t.Error("a path off the read list was sent")
	}
	if len(f.requests) != len(want) {
		t.Errorf("the refused path reached the server: %q", f.requests)
	}
}

// The reviewed list, written out a second time: a new path is a decision.
func TestReadPathsAreTheReviewedList(t *testing.T) {
	if !slices.Equal(readPaths, []string{"/api/pool", "/api/osd", "/api/crush_rule"}) {
		t.Fatalf("readPaths = %q", readPaths)
	}
}

func TestTLSIsVerified(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.CAPEM = otherCA(t)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("read against an unknown certificate: err %v, want a certificate error", err)
	}
	cfg = f.cfg()
	cfg.ServerName = "not-the-dashboard.example"
	c, _ = New(cfg)
	if _, err := c.Read(context.Background()); err == nil {
		t.Fatal("read with a wrong server name succeeded")
	}
}

func TestPlainHTTPAndMissingCAAreRefused(t *testing.T) {
	for name, cfg := range map[string]Config{
		"http":    {BaseURL: "http://dashboard:7000", Username: "u", Password: "p", CAPEM: newFake(t).caPEM()},
		"no CA":   {BaseURL: "https://dashboard:8443", Username: "u", Password: "p"},
		"no pass": {BaseURL: "https://dashboard:8443", Username: "u", CAPEM: newFake(t).caPEM()},
		"path":    {BaseURL: "https://dashboard:8443/api/osd/0/destroy", Username: "u", Password: "p", CAPEM: newFake(t).caPEM()},
		"bad CA":  {BaseURL: "https://dashboard:8443", Username: "u", Password: "p", CAPEM: []byte("not pem")},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExpiredTokenIsRenewedOnce(t *testing.T) {
	f := newFake(t)
	c, _ := New(f.cfg())
	if _, err := c.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.expire = 1
	if _, err := c.Read(context.Background()); err != nil {
		t.Fatalf("read after token expiry: %v", err)
	}
	if f.tokens != 2 {
		t.Errorf("logins = %d, want 2", f.tokens)
	}
	f.expire = 10
	if _, err := c.Read(context.Background()); err == nil {
		t.Error("a persistent 401 did not surface")
	}
}

// otherCA is a self-signed certificate the fake dashboard was not issued by.
func otherCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "other-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ── Reader ─────────────────────────────────────────────────────────────────

func TestReaderStates(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	cfg := f.cfg()
	var loadErr error
	r := &Reader{Load: func(context.Context) (Config, error) { return cfg, loadErr }, Now: func() time.Time { return now }}

	loadErr = ErrNotConfigured
	if p := r.Placement(context.Background()); p.State != "NotConfigured" || len(p.Pools) != 0 {
		t.Fatalf("no Secret: %+v", p)
	}

	loadErr = nil
	now = now.Add(DefaultInterval)
	p := r.Placement(context.Background())
	if p.State != "Collected" || len(p.Pools) != 10 || len(p.OSDs) != 7 || p.OSDs[3].CrushWeight != "1.0900" || p.Pools[0].Name != ".mgr" {
		t.Fatalf("collected: %+v", p)
	}
	collectedAt := p.CollectedAt

	// Within the interval: no new read.
	n := len(f.requests)
	now = now.Add(time.Minute)
	r.Placement(context.Background())
	if len(f.requests) != n {
		t.Errorf("read again within the interval: %q", f.requests[n:])
	}

	// A failed read keeps the last lists, dated, and says why.
	cfg.Password = "wrong"
	now = now.Add(DefaultInterval)
	p = r.Placement(context.Background())
	if p.State != "Error" || len(p.OSDs) != 7 || !p.CollectedAt.Equal(collectedAt) || !strings.Contains(p.Message, "login") {
		t.Fatalf("failed read: state %q, %d OSDs, collectedAt %v, message %q", p.State, len(p.OSDs), p.CollectedAt, p.Message)
	}
	if errors.Is(nil, ErrNotConfigured) {
		t.Fatal("unreachable")
	}
}
