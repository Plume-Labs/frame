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

package redfish

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/Plume-Labs/frame/internal/redfish/redfishtest"
)

const probesPerTest = 5

// The defect: with Basic auth, every request of every probe was an iLO4
// login and logout in its event log. With session reuse, N probes create
// one session, and no request goes out with Basic auth.
func TestSessionReuseCreatesOneSessionForManyProbes(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	c := New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true}, WithSessionReuse())
	for i := range probesPerTest {
		if _, err := c.Probe(context.Background()); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	if got := bmc.Created(); got != 1 {
		t.Fatalf("sessions created over %d probes = %d, want 1", probesPerTest, got)
	}
	if got := bmc.BasicRequests(); got != 0 {
		t.Fatalf("requests sent with Basic auth = %d, want 0", got)
	}
	if bmc.TokenRequests() == 0 {
		t.Fatal("no request carried the session token")
	}
}

// Without the option, New keeps its Basic-auth behaviour: the provisioning
// path (internal/provision) builds short-lived clients that never log out,
// and must not leave a session behind on each run.
func TestNewWithoutTheOptionNeverCreatesASession(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	c := New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true})
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := bmc.Created(); got != 0 {
		t.Fatalf("sessions created = %d, want 0", got)
	}
	if bmc.BasicRequests() == 0 {
		t.Fatal("no request carried Basic auth")
	}
}

// A session that expired or was deleted on the BMC's side answers 401: the
// client logs in again, once, and the probe succeeds.
func TestSessionIsRecreatedWhenTheBMCRejectsTheToken(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	c := New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true}, WithSessionReuse())
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	bmc.ExpireSessions()
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatalf("probe after the session expired: %v", err)
	}
	if got := bmc.Created(); got != 2 {
		t.Fatalf("sessions created = %d, want 2 (the first, and one after the 401)", got)
	}
	if got := bmc.BasicRequests(); got != 0 {
		t.Fatalf("requests sent with Basic auth = %d, want 0", got)
	}
}

// A BMC that accepts the login and then rejects every token must not be
// asked for a new session on every request: one retry, then ErrAuth.
func TestATokenRejectedTwiceIsAnAuthFailureNotALoginLoop(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	bmc.RejectAllTokens()
	c := New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true}, WithSessionReuse())
	_, err := c.Probe(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if got := bmc.Created(); got != 2 {
		t.Fatalf("sessions created = %d, want 2", got)
	}
}

func TestWrongCredentialsOnTheSessionPostAreErrAuth(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	c := New(bmc.URL(), redfishtest.Username, "wrong", &tls.Config{InsecureSkipVerify: true}, WithSessionReuse())
	if _, err := c.Probe(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// A BMC with no SessionService is still probed, over Basic auth, and the
// POST that found out is not repeated on every request.
func TestNoSessionServiceFallsBackToBasicAuthOnce(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	bmc.DisableSessionService()
	c := New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true}, WithSessionReuse())
	for i := range probesPerTest {
		if _, err := c.Probe(context.Background()); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	if got := bmc.SessionPosts(); got != 1 {
		t.Fatalf("session POSTs = %d, want 1", got)
	}
	if bmc.BasicRequests() == 0 {
		t.Fatal("no request carried Basic auth")
	}
}

// Logout deletes the session it created — at this client's own address,
// even though an iLO4's Location header is an absolute URL that could name
// another host — and the next request logs in again.
func TestLogoutDeletesTheSessionAtTheClientsOwnAddress(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	bmc.LocationHost("https://elsewhere.invalid")
	c := New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true}, WithSessionReuse())
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Logout(context.Background()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := bmc.Deleted(); got != 1 {
		t.Fatalf("sessions deleted = %d, want 1", got)
	}
	if got := bmc.Live(); got != 0 {
		t.Fatalf("live sessions after logout = %d, want 0", got)
	}
	if err := c.Logout(context.Background()); err != nil {
		t.Fatalf("second logout: %v", err)
	}
	if got := bmc.Deleted(); got != 1 {
		t.Fatalf("sessions deleted after a second logout = %d, want still 1", got)
	}
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := bmc.Created(); got != 2 {
		t.Fatalf("sessions created = %d, want 2", got)
	}
}

func TestLogoutWithoutASessionSendsNothing(t *testing.T) {
	bmc := redfishtest.NewFakeBMC(t)
	for _, c := range []Client{
		New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true}),
		New(bmc.URL(), redfishtest.Username, redfishtest.Password, &tls.Config{InsecureSkipVerify: true}, WithSessionReuse()),
	} {
		if err := c.Logout(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := bmc.Deleted() + bmc.SessionPosts(); got != 0 {
		t.Fatalf("session requests = %d, want 0", got)
	}
}

func TestSameTarget(t *testing.T) {
	insecure := func() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }
	poolA, poolB := x509.NewCertPool(), x509.NewCertPool()
	withPool := func(p *x509.CertPool) *tls.Config { return &tls.Config{RootCAs: p} }
	base := New("https://bmc.invalid", "u", "p", insecure(), WithSessionReuse())

	cases := []struct {
		name  string
		other Client
		want  bool
	}{
		{"identical, rebuilt", New("https://bmc.invalid", "u", "p", insecure(), WithSessionReuse()), true},
		{"other address", New("https://other.invalid", "u", "p", insecure(), WithSessionReuse()), false},
		{"other user", New("https://bmc.invalid", "v", "p", insecure(), WithSessionReuse()), false},
		{"other password", New("https://bmc.invalid", "u", "q", insecure(), WithSessionReuse()), false},
		{"verification turned on", New("https://bmc.invalid", "u", "p", &tls.Config{}, WithSessionReuse()), false},
		{"without session reuse", New("https://bmc.invalid", "u", "p", insecure()), false},
	}
	for _, tc := range cases {
		if got := SameTarget(base, tc.other); got != tc.want {
			t.Errorf("%s: SameTarget = %v, want %v", tc.name, got, tc.want)
		}
	}

	if !SameTarget(New("https://b", "u", "p", withPool(poolA)), New("https://b", "u", "p", withPool(poolB))) {
		t.Error("two equal CA pools: SameTarget = false, want true")
	}
	poolB.AddCert(&x509.Certificate{Raw: []byte("not-a-real-cert"), RawSubject: []byte("x")})
	if SameTarget(New("https://b", "u", "p", withPool(poolA)), New("https://b", "u", "p", withPool(poolB))) {
		t.Error("different CA pools: SameTarget = true, want false")
	}
}
