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

// Package redfishtest is a fake Redfish BMC for tests: a small but complete
// enough tree for redfish.Client.Probe to succeed, and a SessionService
// that counts what an iLO4 would write to its event log — each session
// created, each session deleted, and each request authenticated with Basic
// auth (which an iLO4 logs as a login and a logout of its own).
package redfishtest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Username and Password are the credentials the fake accepts.
const (
	Username = "admin"
	Password = "secret"
)

const sessionsPath = "/redfish/v1/SessionService/Sessions/"

// documents is the tree the fake serves. Several documents per probe, so a
// test counting sessions against requests has more than one request to
// count.
var documents = map[string]any{
	"/redfish/v1/": map[string]any{
		"Systems":        map[string]string{"@odata.id": "/redfish/v1/Systems/"},
		"Chassis":        map[string]string{"@odata.id": "/redfish/v1/Chassis/"},
		"SessionService": map[string]string{"@odata.id": "/redfish/v1/SessionService/"},
	},
	"/redfish/v1/Systems/": map[string]any{
		"Members": []map[string]string{{"@odata.id": "/redfish/v1/Systems/1/"}},
	},
	"/redfish/v1/Systems/1/": map[string]any{
		"PowerState":    "On",
		"Manufacturer":  "HPE",
		"Model":         "ProLiant ML350 Gen9",
		"MemorySummary": map[string]any{"TotalSystemMemoryGiB": 80},
	},
	"/redfish/v1/Chassis/": map[string]any{
		"Members": []map[string]string{{"@odata.id": "/redfish/v1/Chassis/1/"}},
	},
	"/redfish/v1/Chassis/1/Thermal/": map[string]any{"Temperatures": []any{}, "Fans": []any{}},
	"/redfish/v1/Chassis/1/Power/":   map[string]any{"PowerControl": []any{}},
	"/redfish/v1/Managers/1/":        map[string]any{"FirmwareVersion": "iLO 4 v2.77"},
}

// FakeBMC is a TLS Redfish server. Its counters are safe to read while a
// client is using it.
type FakeBMC struct {
	srv *httptest.Server

	mu               sync.Mutex
	sessions         map[string]string // token -> session id
	created, deleted int
	sessionPosts     int
	basic, token     int
	noSessionService bool
	rejectAllTokens  bool
	locationHost     string
}

// TB is the part of testing.TB NewFakeBMC needs; ginkgo's GinkgoT()
// satisfies it too.
type TB interface {
	Helper()
	Cleanup(func())
}

// NewFakeBMC starts a fake BMC, closed when t ends.
func NewFakeBMC(t TB) *FakeBMC {
	t.Helper()
	b := &FakeBMC{sessions: map[string]string{}}
	b.srv = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

// URL is the fake's base URL.
func (b *FakeBMC) URL() string { return b.srv.URL }

func (b *FakeBMC) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == sessionsPath && r.Method == http.MethodPost:
		b.createSession(w, r)
		return
	case strings.HasPrefix(r.URL.Path, sessionsPath) && r.Method == http.MethodDelete:
		if !b.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b.deleteSession(w, r)
		return
	}

	if !b.authorized(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	doc, ok := documents[r.URL.Path]
	if !ok || r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// authorized counts the request by how it authenticated and says whether
// the BMC accepts it.
func (b *FakeBMC) authorized(r *http.Request) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if tok := r.Header.Get("X-Auth-Token"); tok != "" {
		b.token++
		_, live := b.sessions[tok]
		return live && !b.rejectAllTokens
	}
	if user, pass, ok := r.BasicAuth(); ok {
		b.basic++
		return user == Username && pass == Password
	}
	return false
}

func (b *FakeBMC) createSession(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionPosts++
	if b.noSessionService {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var creds struct {
		UserName string `json:"UserName"`
		Password string `json:"Password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil ||
		creds.UserName != Username || creds.Password != Password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	token, id := randomHex(), randomHex()
	b.sessions[token] = id
	b.created++
	location := sessionsPath + id + "/"
	if b.locationHost != "" {
		location = b.locationHost + location
	}
	w.Header().Set("X-Auth-Token", token)
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"@odata.id": sessionsPath + id + "/"})
}

func (b *FakeBMC) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, sessionsPath), "/")
	b.mu.Lock()
	defer b.mu.Unlock()
	for tok, sid := range b.sessions {
		if sid == id {
			delete(b.sessions, tok)
			b.deleted++
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func randomHex() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (b *FakeBMC) locked(f func() int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return f()
}

// Created counts sessions created: one login each in an iLO4's event log.
func (b *FakeBMC) Created() int { return b.locked(func() int { return b.created }) }

// Deleted counts sessions ended by a DELETE.
func (b *FakeBMC) Deleted() int { return b.locked(func() int { return b.deleted }) }

// Live counts sessions currently open.
func (b *FakeBMC) Live() int { return b.locked(func() int { return len(b.sessions) }) }

// SessionPosts counts session POSTs, answered or not.
func (b *FakeBMC) SessionPosts() int { return b.locked(func() int { return b.sessionPosts }) }

// BasicRequests counts requests authenticated with Basic auth: one login
// and one logout each in an iLO4's event log.
func (b *FakeBMC) BasicRequests() int { return b.locked(func() int { return b.basic }) }

// TokenRequests counts requests that carried an X-Auth-Token.
func (b *FakeBMC) TokenRequests() int { return b.locked(func() int { return b.token }) }

// ExpireSessions drops every open session without telling the clients, the
// way an idle timeout or an iLO reset does.
func (b *FakeBMC) ExpireSessions() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions = map[string]string{}
}

// RejectAllTokens makes every token-authenticated request answer 401.
func (b *FakeBMC) RejectAllTokens() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rejectAllTokens = true
}

// DisableSessionService makes the session POST answer 404, as on a BMC
// with no SessionService.
func (b *FakeBMC) DisableSessionService() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.noSessionService = true
}

// LocationHost makes the session POST answer with an absolute Location on
// host (scheme included), as an iLO4 does.
func (b *FakeBMC) LocationHost(host string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.locationHost = host
}
