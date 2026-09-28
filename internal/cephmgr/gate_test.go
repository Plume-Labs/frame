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
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// stateGate is the real SecretLoginGate over a fake API: with or without
// the state Secret, with optional injected failures.
func stateGate(t *testing.T, withSecret bool, fns interceptor.Funcs) (*SecretLoginGate, client.WithWatch) {
	t.Helper()
	b := fake.NewClientBuilder()
	if withSecret {
		b = b.WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: StateSecretName, Namespace: "frame-system"}})
	}
	c := interceptor.NewClient(b.Build(), fns)
	return &SecretLoginGate{Reader: c, Writer: c, Namespace: "frame-system"}, c
}

func stateSecret(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	var sec corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "frame-system", Name: StateSecretName}, &sec); err != nil {
		t.Fatal(err)
	}
	return &sec
}

func setData(t *testing.T, c client.Client, data map[string][]byte) {
	t.Helper()
	sec := stateSecret(t, c)
	sec.Data = data
	if err := c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
}

// process is one manager process: a fresh Reader on the shared gate.
func process(cfg *Config, gate LoginGate, now *time.Time) *Reader {
	return &Reader{Load: func(context.Context) (Config, error) { return *cfg, nil }, Gate: gate, Now: func() time.Time { return *now }}
}

var t0 = time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC)

// The intention is written before the login is sent: an account refused
// once is never tried again, across processes (restarts, a new leader).
func TestRefusedAccountIsNeverTriedAgain(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	gate, c := stateGate(t, true, interceptor.Funcs{})
	now := t0
	for i := 0; i < 6; i++ {
		now = now.Add(DefaultInterval)
		if p := place(process(&cfg, gate, &now)); p.State != "AuthRefused" {
			t.Fatalf("process %d: state %q (%s)", i, p.State, p.Message)
		}
	}
	if f.logins != 1 {
		t.Fatalf("%d logins across 6 processes, want 1", f.logins)
	}
	d := stateSecret(t, c).Data
	if len(d["hmac-key"]) != 32 || string(d["pending"]) != "" || len(parsedList(t, d["refused"])) != 1 {
		t.Fatalf("state after a refusal: %v", d)
	}
}

func parsedList(t *testing.T, raw []byte) []string {
	l, err := parseList(raw)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// A successful login leaves nothing pending, and the next process logs in.
func TestSuccessfulLoginClearsItsIntention(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	gate, c := stateGate(t, true, interceptor.Funcs{})
	now := t0
	if p := place(process(&cfg, gate, &now)); p.State != "Collected" {
		t.Fatalf("state %q (%s)", p.State, p.Message)
	}
	if d := stateSecret(t, c).Data; string(d["pending"]) != "" || string(d["refused"]) != "" {
		t.Fatalf("state after a success: %v", d)
	}
	now = now.Add(DefaultInterval)
	if p := place(process(&cfg, gate, &now)); p.State != "Collected" || f.logins != 2 {
		t.Fatalf("next process: state %q, %d logins", p.State, f.logins)
	}
}

// Crash after the intention, before the login: the next process does not
// log in (outcome unknown).
func TestCrashAfterIntentionBeforeLogin(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	gate, _ := stateGate(t, true, interceptor.Funcs{})
	if err := gate.Begin(context.Background(), cfg); err != nil { // then the process dies
		t.Fatal(err)
	}
	now := t0
	p := place(process(&cfg, gate, &now))
	if f.logins != 0 || p.State != "Error" || !strings.Contains(p.Message, "outcome unknown") {
		t.Fatalf("%d logins, state %q (%s)", f.logins, p.State, p.Message)
	}
}

// Crash between the login and the write of its outcome (simulated: the
// write of the refusal fails): the intention stays pending, and the next
// process does not log in.
func TestCrashBetweenLoginAndOutcome(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	var updates atomic.Int32
	gate, _ := stateGate(t, true, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if updates.Add(1) > 1 { // the intention lands, nothing after it
				return errors.New("process gone")
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	now := t0
	place(process(&cfg, gate, &now))
	for i := 0; i < 3; i++ {
		now = now.Add(DefaultInterval)
		place(process(&cfg, gate, &now))
	}
	if f.logins != 1 {
		t.Fatalf("%d logins, want 1", f.logins)
	}
}

// A write that conflicts is re-read and retried; the intention lands once,
// the refusal is recorded, and a restart does not log in.
func TestConflictingWriteIsRetriedThenRestart(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	var conflicts atomic.Int32
	gate, _ := stateGate(t, true, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if conflicts.Add(1) == 1 {
				return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, StateSecretName, errors.New("stale"))
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	now := t0
	// The conflict is re-read and retried within the same pass: the login
	// goes out once, and is recorded as refused.
	if p := place(process(&cfg, gate, &now)); p.State != "AuthRefused" || f.logins != 1 {
		t.Fatalf("first process: state %q (%s), %d logins", p.State, p.Message, f.logins)
	}
	for i := 0; i < 3; i++ {
		now = now.Add(DefaultInterval)
		place(process(&cfg, gate, &now))
	}
	if f.logins != 1 {
		t.Fatalf("%d failed logins in total, want 1", f.logins)
	}
}

// Every write failing: no login at all.
func TestIntentionThatCannotBeWrittenMeansNoLogin(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	gate, _ := stateGate(t, true, interceptor.Funcs{
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			return errors.New("forbidden")
		},
	})
	now := t0
	if p := place(process(&cfg, gate, &now)); f.logins != 0 || p.State != "Error" {
		t.Fatalf("%d logins, state %q", f.logins, p.State)
	}
}

// Every corrupt shape of the state: no login, "state Secret corrupt".
func TestCorruptStateMeansNoLogin(t *testing.T) {
	good, _ := newKey()
	other, _ := newKey()
	mac := strings.Repeat("ab", 32)
	for name, data := range map[string]map[string][]byte{
		"key of the wrong size":         {"hmac-key": good[:16], "key-fp": []byte(keyFingerprint(good[:16]))},
		"fingerprint mismatch":          {"hmac-key": good, "key-fp": []byte("0000000000000000")},
		"key replaced by a third party": {"hmac-key": other, "key-fp": []byte(keyFingerprint(good)), "refused": []byte(mac)},
		"lists without a key":           {"refused": []byte(mac)},
		"fingerprint without a key":     {"key-fp": []byte(keyFingerprint(good))},
		"unparsable refused":            {"hmac-key": good, "key-fp": []byte(keyFingerprint(good)), "refused": []byte("not-a-mac")},
		"unparsable pending":            {"hmac-key": good, "key-fp": []byte(keyFingerprint(good)), "pending": []byte(mac + "\nzz")},
	} {
		f := newFake(t)
		cfg := f.cfg()
		gate, c := stateGate(t, true, interceptor.Funcs{})
		setData(t, c, data)
		now := t0
		p := place(process(&cfg, gate, &now))
		if f.logins != 0 || p.State != "Error" || !strings.Contains(p.Message, "state Secret corrupt") {
			t.Errorf("%s: %d logins, state %q (%s)", name, f.logins, p.State, p.Message)
		}
	}
}

// A, then B, then A again: A is still known refused.
func TestAThenBThenA(t *testing.T) {
	f := newFake(t)
	a, b := f.cfg(), f.cfg()
	a.Password, b.Password = "wrong-a", "wrong-b"
	gate, _ := stateGate(t, true, interceptor.Funcs{})
	now := t0
	for _, cfg := range []Config{a, b, a} {
		cfg := cfg
		now = now.Add(DefaultInterval)
		place(process(&cfg, gate, &now))
	}
	if f.logins != 2 {
		t.Fatalf("%d logins for A, B, A, want 2", f.logins)
	}
}

// No state Secret: five processes, no login.
func TestMissingStateSecretMeansNoLogin(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	gate, _ := stateGate(t, false, interceptor.Funcs{})
	now := t0
	for i := 0; i < 5; i++ {
		now = now.Add(DefaultInterval)
		p := place(process(&cfg, gate, &now))
		if p.State != "Error" || !strings.Contains(p.Message, "state Secret missing: frame-system/frame-ceph-dashboard-state") {
			t.Fatalf("process %d: state %q (%s)", i, p.State, p.Message)
		}
	}
	if f.logins != 0 {
		t.Fatalf("%d logins without a state Secret, want 0", f.logins)
	}
}

// A network error or a 5xx leaves the intention pending: no retry.
func TestNetworkErrorLeavesTheIntentionPending(t *testing.T) {
	f := newFake(t)
	f.authFail = http.StatusServiceUnavailable
	cfg := f.cfg()
	gate, c := stateGate(t, true, interceptor.Funcs{})
	now := t0
	place(process(&cfg, gate, &now))
	f.authFail = 0
	now = now.Add(DefaultInterval)
	p := place(process(&cfg, gate, &now))
	if f.logins != 1 || !strings.Contains(p.Message, "outcome unknown") || len(parsedList(t, stateSecret(t, c).Data["pending"])) != 1 {
		t.Fatalf("%d logins, state %q (%s)", f.logins, p.State, p.Message)
	}
}

// The key is generated only in an entirely empty Secret, with the first
// intention; lists are bounded, oldest out first.
func TestKeyGenerationAndBoundedLists(t *testing.T) {
	gate, c := stateGate(t, true, interceptor.Funcs{})
	ctx := context.Background()
	for i := 0; i < MaxRefused+2; i++ {
		cfg := Config{BaseURL: "https://d", Username: "u", Password: fmt.Sprint(i)}
		if err := gate.Begin(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if err := gate.Refused(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}
	d := stateSecret(t, c).Data
	if n := len(parsedList(t, d["refused"])); n != MaxRefused {
		t.Fatalf("%d refused kept, want %d", n, MaxRefused)
	}
	if err := gate.Begin(ctx, Config{BaseURL: "https://d", Username: "u", Password: "0"}); err != nil {
		t.Errorf("the oldest refusal was not the one dropped: %v", err)
	}
	if err := gate.Begin(ctx, Config{BaseURL: "https://d", Username: "u", Password: fmt.Sprint(MaxRefused + 1)}); !errors.Is(err, ErrLoginRefusedBefore) {
		t.Errorf("the newest refusal is missing: %v", err)
	}
	if len(d["hmac-key"]) != 32 || string(d["key-fp"]) != keyFingerprint(d["hmac-key"]) {
		t.Fatalf("key %d bytes, fp %q", len(d["hmac-key"]), d["key-fp"])
	}
}

// A 400 that is not invalid_credentials is an error, not a refusal — and
// its outcome is not a refusal, so it stays pending (no retry either).
func TestOther400IsNotARefusal(t *testing.T) {
	f := newFake(t)
	f.authFail = http.StatusBadRequest
	cfg := f.cfg()
	gate, c := stateGate(t, true, interceptor.Funcs{})
	now := t0
	if p := place(process(&cfg, gate, &now)); p.State != "Error" || string(stateSecret(t, c).Data["refused"]) != "" {
		t.Fatalf("state %q, refused %q", p.State, stateSecret(t, c).Data["refused"])
	}
}

// The stored value depends on the key.
func TestMACIsKeyed(t *testing.T) {
	cfg := Config{BaseURL: "https://dash:8443", Username: "u", Password: "p"}
	k1, _ := newKey()
	k2, _ := newKey()
	if accountMAC(k1, cfg) == accountMAC(k2, cfg) {
		t.Fatal("same MAC under two keys")
	}
}

// A reader without a gate never logs in.
func TestNoGateNoLogin(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	now := t0
	if p := place(process(&cfg, nil, &now)); f.logins != 0 || p.State != "Error" {
		t.Fatalf("%d logins, state %q", f.logins, p.State)
	}
}

// The state Secret emptied while the login is in flight: the refusal is
// written again under a new key, and nothing retries.
func TestSecretEmptiedDuringTheLogin(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	gate, c := stateGate(t, true, interceptor.Funcs{})
	f.onLogin = func() { setData(t, c, nil) }
	now := t0
	if p := place(process(&cfg, gate, &now)); p.State != "AuthRefused" {
		t.Fatalf("state %q (%s)", p.State, p.Message)
	}
	f.onLogin = nil
	for i := 0; i < 3; i++ {
		now = now.Add(DefaultInterval)
		place(process(&cfg, gate, &now))
	}
	if f.logins != 1 {
		t.Fatalf("%d failed logins, want 1", f.logins)
	}
}

// The state Secret deleted while the login is in flight: the refusal
// cannot be recorded, and the status says so.
func TestSecretDeletedDuringTheLogin(t *testing.T) {
	f := newFake(t)
	cfg := f.cfg()
	cfg.Password = "wrong"
	gate, c := stateGate(t, true, interceptor.Funcs{})
	f.onLogin = func() { _ = c.Delete(context.Background(), stateSecret(t, c)) }
	now := t0
	p := place(process(&cfg, gate, &now))
	if p.State != "AuthRefused" || !strings.Contains(p.Message, "refusal not recorded") || !strings.Contains(p.Message, "do not retry before fixing the password") {
		t.Fatalf("state %q (%s)", p.State, p.Message)
	}
}

// A login that provably never left — connection refused, TLS handshake
// failed — does not count: its intention is removed, nothing is blocked.
func TestLoginNotSentIsNotBlocking(t *testing.T) {
	f := newFake(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "https://" + l.Addr().String()
	_ = l.Close()
	for name, mutate := range map[string]func(*Config){
		"connection refused":   func(c *Config) { c.BaseURL = closed },
		"TLS handshake failed": func(c *Config) { c.ServerName = "not-the-dashboard.example" },
	} {
		cfg := f.cfg()
		mutate(&cfg)
		gate, c := stateGate(t, true, interceptor.Funcs{})
		now := t0
		p := place(process(&cfg, gate, &now))
		if pending := parsedList(t, stateSecret(t, c).Data["pending"]); len(pending) != 0 || p.State != "Error" {
			t.Errorf("%s: pending %v, state %q (%s)", name, pending, p.State, p.Message)
		}
		// And the next pass tries again rather than reporting "outcome unknown".
		now = now.Add(DefaultInterval)
		if p := place(process(&cfg, gate, &now)); strings.Contains(p.Message, "outcome unknown") {
			t.Errorf("%s: blocked after a login that was never sent: %s", name, p.Message)
		}
	}
	if f.logins != 0 {
		t.Fatalf("the dashboard saw %d logins", f.logins)
	}
}

// A login that did leave — the dashboard read it, then the connection
// dropped with no answer — has an unknown outcome: it stays blocking.
func TestLoginSentThenDroppedStaysBlocking(t *testing.T) {
	f := newFake(t)
	f.authDrop = true
	cfg := f.cfg()
	gate, c := stateGate(t, true, interceptor.Funcs{})
	now := t0
	place(process(&cfg, gate, &now))
	if pending := parsedList(t, stateSecret(t, c).Data["pending"]); len(pending) != 1 {
		t.Fatalf("pending %v, want the sent login kept", pending)
	}
	f.authDrop = false
	now = now.Add(DefaultInterval)
	if p := place(process(&cfg, gate, &now)); f.logins != 1 || !strings.Contains(p.Message, "outcome unknown") {
		t.Fatalf("%d logins, state %q (%s)", f.logins, p.State, p.Message)
	}
}

// Two leaders at once (a lease handover), same wrong account: at most one
// failed login between them.
func TestTwoConcurrentLeaders(t *testing.T) {
	for round := 0; round < 10; round++ {
		f := newFake(t)
		cfg := f.cfg()
		cfg.Password = "wrong"
		gate, _ := stateGate(t, true, interceptor.Funcs{})
		now := t0
		a, b := process(&cfg, gate, &now), process(&cfg, gate, &now)
		var wg sync.WaitGroup
		for _, r := range []*Reader{a, b} {
			wg.Add(1)
			go func(r *Reader) { defer wg.Done(); place(r) }(r)
		}
		wg.Wait()
		if f.logins > 1 {
			t.Fatalf("round %d: %d failed logins from two leaders, want at most 1", round, f.logins)
		}
	}
}

// pending is never trimmed: a full list forbids every new login (fail
// closed) instead of forgetting an account whose outcome is unknown.
func TestFullPendingMeansNoLogin(t *testing.T) {
	gate, c := stateGate(t, true, interceptor.Funcs{})
	ctx := context.Background()
	for i := 0; i < MaxRefused; i++ {
		if err := gate.Begin(ctx, Config{BaseURL: "https://d", Username: "u", Password: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	f := newFake(t)
	cfg := f.cfg()
	now := t0
	p := place(process(&cfg, gate, &now))
	if f.logins != 0 || p.State != "Error" || !strings.Contains(p.Message, "too many unresolved logins") {
		t.Fatalf("full pending: %d login(s), state %s %q", f.logins, p.State, p.Message)
	}
	pending := parsedList(t, stateSecret(t, c).Data["pending"])
	if len(pending) != MaxRefused {
		t.Fatalf("%d pending kept, want %d", len(pending), MaxRefused)
	}
	if err := gate.Begin(ctx, Config{BaseURL: "https://d", Username: "u", Password: "0"}); !errors.Is(err, ErrLoginOutcomeUnknown) {
		t.Errorf("the oldest unresolved login was forgotten: %v", err)
	}
}

// Ceph counts failed logins per user, whatever the URL: a refusal blocks
// the same credentials through any other address of the dashboard.
func TestRefusalHoldsAcrossURLs(t *testing.T) {
	f := newFake(t)
	gate, _ := stateGate(t, true, interceptor.Funcs{})
	now := t0
	cfg := f.cfg()
	cfg.Password = "wrong"
	if p := place(process(&cfg, gate, &now)); p.State != "AuthRefused" || p.CertificateNotAfter == nil {
		t.Fatalf("first URL: %s %q, certificate expiry %v", p.State, p.Message, p.CertificateNotAfter)
	}
	for _, u := range []string{f.srv.URL + "/", strings.Replace(f.srv.URL, "127.0.0.1", "localhost", 1), strings.ToUpper(f.srv.URL)} {
		other := cfg
		other.BaseURL = u
		now = now.Add(DefaultInterval)
		if p := place(process(&other, gate, &now)); p.State != "AuthRefused" {
			t.Fatalf("%s: %s %q", u, p.State, p.Message)
		}
	}
	if f.logins != 1 {
		t.Fatalf("%d logins for one refused account across URLs, want 1", f.logins)
	}
}
