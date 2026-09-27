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
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

// fakeDashboard serves the fixtures (anonymised) the way Ceph v20.2.2's
// dashboard does, including what the client must get right:
//   - each endpoint's API version, checked on the Accept header like
//     BaseController does (same major, client minor <= endpoint minor,
//     415 otherwise): pool 1.0, osd 1.1, crush_rule 2.0, auth 1.0;
//   - /api/osd paginated, limit=10 by default, limit=-1 for all, with
//     X-Total-Count;
//   - a refused login answered as Ceph does: HTTP 400 with
//     {"code": "invalid_credentials"} (auth.py, InvalidCredentialsError);
//   - optionally a redirect on /api/auth.
//
// It records every request.
type fakeDashboard struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []string
	expire   int // answer 401 to the next n authenticated GETs
	tokens   int
	logins   int
	redirect bool   // answer 307 on /api/auth
	password string // the password the account has now
	authFail int    // a status to answer every login with (0: normal)
	fail     map[string]int
}

var fakeVersions = map[string][2]int{"/api/pool": {1, 0}, "/api/osd": {1, 1}, "/api/crush_rule": {2, 0}, "/api/auth": {1, 0}}

func versionOK(accept, path string) bool {
	v, ok := fakeVersions[path]
	if !ok {
		return false
	}
	var major, minor int
	if _, err := fmt.Sscanf(accept, "application/vnd.ceph.api.v%d.%d+json", &major, &minor); err != nil {
		return false
	}
	return major == v[0] && minor <= v[1]
}

func newFake(t *testing.T) *fakeDashboard {
	f := &fakeDashboard{t: t, fail: map[string]int{}, password: "s3cret"}
	files := map[string]string{PathPools: "pools.json", PathOSDs: "osds.json", PathCrushRules: "crush_rules.json"}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		if !versionOK(r.Header.Get("Accept"), r.URL.Path) {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == pathAuth {
			f.mu.Lock()
			f.logins++
			f.mu.Unlock()
			if f.redirect {
				http.Redirect(w, r, "/api/auth-elsewhere", http.StatusTemporaryRedirect)
				return
			}
			if f.authFail != 0 {
				w.WriteHeader(f.authFail)
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			good := body["username"] == "frame-storage-reader" && body["password"] == f.password
			f.mu.Unlock()
			if !good {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"component": "auth", "code": "invalid_credentials", "detail": "Invalid credentials"}`))
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
		if code := f.fail[r.URL.Path]; code != 0 {
			f.mu.Unlock()
			w.WriteHeader(code)
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
		if r.URL.Path == PathOSDs {
			var all []json.RawMessage
			_ = json.Unmarshal(b, &all)
			limit := 10
			if l := r.URL.Query().Get("limit"); l != "" {
				_, _ = fmt.Sscanf(l, "%d", &limit)
			}
			page := all
			if limit >= 0 && limit < len(all) {
				page = all[:limit]
			}
			w.Header().Set("X-Total-Count", fmt.Sprint(len(all)))
			b, _ = json.Marshal(page)
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
	// 12 OSDs in the fixture, more than the dashboard's default page of 10:
	// all must come, osd.7 excepted (not in the CRUSH map: absent, not 0).
	if len(snap.OSDs) != 11 || len(snap.Errors) != 0 {
		t.Fatalf("OSDs with a CRUSH weight = %d (errors %v), want 11", len(snap.OSDs), snap.Errors)
	}
	if last := snap.OSDs[10]; last.ID != 11 || last.CrushWeight != 0 {
		t.Errorf("osd.11 = %+v, want weight 0 (a real 0)", last)
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
	want := []string{"POST /api/auth", "GET /api/pool", "GET /api/osd?limit=-1", "GET /api/crush_rule"}
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

// The reviewed list, written out a second time: a new endpoint, query or
// version is a decision.
func TestReadEndpointsAreTheReviewedList(t *testing.T) {
	want := []endpoint{
		{Path: "/api/pool", Version: "1.0"},
		{Path: "/api/osd", Query: "limit=-1", Version: "1.1"},
		{Path: "/api/crush_rule", Version: "2.0"},
	}
	if !slices.Equal(readEndpoints, want) {
		t.Fatalf("readEndpoints = %+v", readEndpoints)
	}
}

// One endpoint failing (here the dashboard rejects the rule list) must not
// discard the pools and OSDs read alongside.
func TestOneEndpointFailingKeepsTheOthers(t *testing.T) {
	f := newFake(t)
	f.fail[PathCrushRules] = http.StatusUnsupportedMediaType
	c, _ := New(f.cfg())
	snap, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pools) != 10 || len(snap.OSDs) != 11 || snap.Errors[PathCrushRules] == nil {
		t.Fatalf("pools %d, osds %d, errors %v", len(snap.Pools), len(snap.OSDs), snap.Errors)
	}
	if snap.Pools[1].CrushRule != "hdd2_ssd2" {
		t.Errorf("rule without the rule list = %q, want the name alone", snap.Pools[1].CrushRule)
	}
}

// A redirect on /api/auth would re-send the password to wherever it points.
func TestLoginRedirectIsNotFollowed(t *testing.T) {
	f := newFake(t)
	f.redirect = true
	c, _ := New(f.cfg())
	if _, err := c.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("err %v, want the 307 surfaced", err)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "auth-elsewhere") {
			t.Fatalf("the redirect was followed: %q", f.requests)
		}
	}
}

// Error text reaches status.message: never the Secret's credentials or query.
func TestURLErrorsNameSchemeAndHostOnly(t *testing.T) {
	for _, raw := range []string{"http://Xuser:Xpw@dash:7000/?Xtok=Xabc#Xfrag", "https://Xuser:Xpw@dash:8443/Xpath?Xtok=Xabc#Xfrag", "https://Xuser:Xpw@dash:8443"} {
		_, err := New(Config{BaseURL: raw, Username: "u", Password: "p", CAPEM: newFake(t).caPEM()})
		if err == nil {
			t.Fatalf("%s accepted", raw)
		}
		for _, leak := range []string{"Xuser", "Xpw", "Xtok", "Xabc", "Xfrag", "Xpath"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%q leaks %q", err.Error(), leak)
			}
		}
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
	if snap, err := c.Read(context.Background()); err == nil && len(snap.Errors) == 0 {
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

// place calls Placement and waits for the refresh it may have started.
func place(r *Reader) *framev1beta1.CephPlacement {
	r.Placement(context.Background(), nil)
	r.wait()
	return r.Placement(context.Background(), nil)
}

func TestReaderStates(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	cfg := f.cfg()
	var loadErr error
	r := &Reader{Load: func(context.Context) (Config, error) { return cfg, loadErr }, Now: func() time.Time { return now }}

	loadErr = ErrNotConfigured
	if p := place(r); p.State != "NotConfigured" || len(p.Pools) != 0 {
		t.Fatalf("no Secret: %+v", p)
	}

	loadErr = nil
	now = now.Add(DefaultInterval)
	p := place(r)
	if p.State != "Collected" || len(p.Pools) != 10 || len(p.OSDs) != 11 || p.OSDs[3].CrushWeight != "1.0900" || p.Pools[0].Name != ".mgr" || p.Pools[1].CrushRule != "20 hdd2_ssd2" {
		t.Fatalf("collected: %+v", p)
	}
	collectedAt := p.CollectedAt

	// Within the interval: no new read.
	n := len(f.requests)
	now = now.Add(time.Minute)
	place(r)
	if len(f.requests) != n {
		t.Errorf("read again within the interval: %q", f.requests[n:])
	}

	// A partial failure keeps what was read and says what was not.
	f.fail[PathOSDs] = http.StatusInternalServerError
	now = now.Add(DefaultInterval)
	p = place(r)
	if p.State != "Error" || !strings.Contains(p.Message, "partial read") || len(p.OSDs) != 11 || len(p.Pools) != 10 {
		t.Fatalf("partial: state %q, message %q, %d OSDs", p.State, p.Message, len(p.OSDs))
	}
	// Each list carries its own date: pools fresh, OSDs from the last pass.
	if p.PoolsReadAt == nil || !p.PoolsReadAt.Time.Equal(now) || p.OSDsReadAt == nil || p.OSDsReadAt.Time.Equal(now) || !p.CollectedAt.Equal(collectedAt) {
		t.Fatalf("partial dates: pools %v, osds %v, collected %v (last complete %v)", p.PoolsReadAt, p.OSDsReadAt, p.CollectedAt, collectedAt)
	}
	delete(f.fail, PathOSDs)

	// Every GET failing after a good login: nothing fresh, the last read's
	// date is kept.
	for _, p := range []string{PathPools, PathOSDs, PathCrushRules} {
		f.fail[p] = http.StatusInternalServerError
	}
	now = now.Add(DefaultInterval)
	p = place(r)
	if p.State != "Error" || !p.CollectedAt.Equal(collectedAt) || len(p.OSDs) != 11 {
		t.Fatalf("all GETs failing: state %q, collectedAt %v (want %v)", p.State, p.CollectedAt, collectedAt)
	}
	f.fail = map[string]int{}

	// The rule list failing alone: pools and OSDs fresh, the rules' date is
	// the previous one.
	f.fail[PathCrushRules] = http.StatusUnsupportedMediaType
	rulesBefore := p.RulesReadAt
	now = now.Add(DefaultInterval)
	p = place(r)
	if p.RulesReadAt == nil || !p.RulesReadAt.Equal(rulesBefore) || !p.PoolsReadAt.Time.Equal(now) {
		t.Fatalf("rules failing: rulesReadAt %v (was %v), poolsReadAt %v", p.RulesReadAt, rulesBefore, p.PoolsReadAt)
	}
	f.fail = map[string]int{}

	// A transport failure keeps the last lists, dated.
	cfg.ServerName = "wrong.example"
	now = now.Add(DefaultInterval)
	p = place(r)
	if p.State != "Error" || len(p.OSDs) != 11 || !p.CollectedAt.Equal(collectedAt) {
		t.Fatalf("failed read: state %q, %d OSDs, collectedAt %v, message %q", p.State, len(p.OSDs), p.CollectedAt, p.Message)
	}
}

// The reconcile asking never waits on the network.
func TestPlacementNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	var loads atomic.Int32
	r := &Reader{Load: func(context.Context) (Config, error) { loads.Add(1); <-release; return Config{}, ErrNotConfigured }}
	start := time.Now()
	if p := r.Placement(context.Background(), nil); p != nil {
		t.Errorf("first call returned %+v before any read completed", p)
	}
	r.Placement(context.Background(), nil)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("Placement blocked for %s", time.Since(start))
	}
	time.Sleep(50 * time.Millisecond)
	if n := loads.Load(); n != 1 {
		t.Errorf("%d refreshes started for two calls during one read, want 1", n)
	}
	close(release)
	r.wait()
	if p := r.Placement(context.Background(), nil); p == nil || p.State != "NotConfigured" {
		t.Fatalf("after the read: %+v", p)
	}
}

// Ceph answers a refused login with 400 invalid_credentials: that is a
// refusal, not a transient error.
func TestRefusedLoginIsRecognised(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	c, _ := New(cfg)
	if _, err := c.Read(context.Background()); !errors.Is(err, ErrAuthRefused) {
		t.Fatalf("err %v, want ErrAuthRefused", err)
	}
}

// Once a login fails, the pass stops logging in: an expired token met on
// every GET must not turn into one refused login per GET.
func TestAFailedLoginEndsThePass(t *testing.T) {
	f := newFake(t)
	c, _ := New(f.cfg())
	if _, err := c.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.expire, f.password = 100, "changed-on-the-dashboard"
	before := f.logins
	f.mu.Unlock()
	if _, err := c.Read(context.Background()); !errors.Is(err, ErrAuthRefused) {
		t.Fatalf("err %v, want ErrAuthRefused", err)
	}
	if n := f.logins - before; n != 1 {
		t.Fatalf("%d logins in the pass, want 1", n)
	}

	// A login failing for another reason (the dashboard answering 500)
	// does not stop Read, which goes on to the next endpoint: with a token
	// that expires mid-pass, those GETs must not log in again either.
	f.mu.Lock()
	f.password, f.expire, f.authFail = "s3cret", 0, 0
	f.mu.Unlock()
	if _, err := c.Read(context.Background()); err != nil { // a fresh, valid token
		t.Fatal(err)
	}
	f.mu.Lock()
	f.expire, f.authFail = 100, http.StatusInternalServerError
	before = f.logins
	f.mu.Unlock()
	snap, err := c.Read(context.Background())
	if err != nil || len(snap.Errors) != 3 {
		t.Fatalf("err %v, errors %v", err, snap)
	}
	if n := f.logins - before; n != 1 {
		t.Fatalf("%d logins in a pass whose login keeps failing, want 1", n)
	}
}

// After a refusal: no login at all, however long, until the account's
// content in the Secret changes. An annotation (a new resourceVersion)
// changes nothing.
func TestRefusedAccountIsNeverRetriedUntilItChanges(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	cfg := f.cfg()
	cfg.Password, cfg.Version = "wrong", "1"
	r := &Reader{Load: func(context.Context) (Config, error) { return cfg, nil }, Now: func() time.Time { return now }}

	p := place(r)
	if p.State != "AuthRefused" || f.logins != 1 || p.RefusedCredentials == "" || strings.Contains(p.RefusedCredentials, "wrong") {
		t.Fatalf("state %q, %d login(s), refused %q", p.State, f.logins, p.RefusedCredentials)
	}
	for i := 0; i < 24*12; i++ { // a day of 5-minute passes
		now = now.Add(DefaultInterval)
		if i == 100 {
			cfg.Version = "2" // an annotation on the Secret
		}
		place(r)
	}
	if f.logins != 1 {
		t.Fatalf("%d logins in a day with a refused account, want 1", f.logins)
	}
	cfg.Password = "s3cret"
	now = now.Add(DefaultInterval)
	if p := place(r); p.State != "Collected" || f.logins != 2 {
		t.Fatalf("after the password changed: state %q, %d logins", p.State, f.logins)
	}
}

// A restarted manager reads the refusal back from status: no login with
// the refused account.
func TestRefusalSurvivesARestart(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	first := &Reader{Load: func(context.Context) (Config, error) { return cfg, nil }}
	persisted := place(first)
	if persisted.State != "AuthRefused" {
		t.Fatalf("state %q", persisted.State)
	}

	restarted := &Reader{Load: func(context.Context) (Config, error) { return cfg, nil }}
	restarted.Placement(context.Background(), persisted)
	restarted.wait()
	p := restarted.Placement(context.Background(), persisted)
	if p.State != "AuthRefused" || f.logins != 1 {
		t.Fatalf("after restart: state %q, %d logins, want AuthRefused and still 1", p.State, f.logins)
	}
}

// A panic inside a read ends as an Error state and the reader keeps going.
func TestPanicInAReadEndsAsError(t *testing.T) {
	calls := 0
	r := &Reader{Load: func(context.Context) (Config, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		return Config{}, ErrNotConfigured
	}}
	if p := place(r); p == nil || p.State != "Error" || !strings.Contains(p.Message, "boom") {
		t.Fatalf("after a panic: %+v", p)
	}
	r.lastAt = time.Time{}
	if p := place(r); p.State != "NotConfigured" {
		t.Fatalf("reader did not recover: %+v", p)
	}
}
