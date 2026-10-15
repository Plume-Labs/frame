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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The login gate. Ceph counts failed dashboard logins per user and only a
// successful login resets the count, so no login may happen that the
// cluster could forget: the intention of every login is written, durably,
// BEFORE the login is sent (write-ahead).
//
// The state Secret (StateSecretName) holds:
//   - hmac-key: exactly 32 random bytes;
//   - key-fp:   hex of the first 8 bytes of SHA-256(hmac-key), so a key
//     replaced or damaged by hand is detected rather than trusted;
//   - pending:  MACs of accounts whose login was started and whose outcome
//     is not known (yet, or ever: a crash, a network error);
//   - refused:  MACs of accounts the dashboard refused.
//
// MAC = HMAC-SHA256(hmac-key, username \x00 password), hex, one per line —
// no URL: Ceph counts failed logins per user. At most MaxRefused per list:
// refused drops its oldest first, a full pending forbids every login.
//
// A MAC in pending or refused forbids the login; the operator unblocks
// (docs/storage-mgr-account.md). A state that cannot be read, is missing,
// or fails strict validation forbids every login. The key is generated only
// when the Secret is entirely empty, in the same Update as the first
// intention.

// StateSecretName is the dedicated Secret, in the manager's namespace. The
// deployment manifests create it empty, so the manager needs only get and
// update on this one name (config/manager/ceph_dashboard_state.yaml,
// charts/frame/templates/ceph-dashboard-state.yaml).
const StateSecretName = "frame-ceph-dashboard-state"

// MaxRefused bounds each list. refused is a FIFO: past it the oldest entry
// is forgotten, and that account could cost one more failed login if it
// came back. pending is not: a full pending list forbids every new login
// (ErrTooManyUnresolved) — dropping an entry would unblock an account whose
// last login has an unknown outcome.
const MaxRefused = 64

// writeAttempts bounds the Get/validate/Update rounds of one write (a
// conflict with another writer is re-read and retried).
const writeAttempts = 3

var (
	// ErrStateMissing: the state Secret does not exist (never created by
	// the manager).
	ErrStateMissing = errors.New("state Secret missing")
	// ErrStateCorrupt: the state Secret fails strict validation.
	ErrStateCorrupt = errors.New("state Secret corrupt")
	// ErrLoginRefusedBefore: the account is in refused.
	ErrLoginRefusedBefore = errors.New("account refused before")
	// ErrLoginOutcomeUnknown: the account is in pending — a login was
	// started and its outcome was never recorded.
	ErrLoginOutcomeUnknown = errors.New("outcome of a previous login unknown")
	// ErrTooManyUnresolved: pending is full; no new login until an
	// operator clears it.
	ErrTooManyUnresolved = errors.New("too many unresolved logins, clear pending (see docs/storage-mgr-account.md)")
)

// LoginGate is what the client asks before and tells after every login.
type LoginGate interface {
	// Begin writes the intention to log in with c; it returns nil only once
	// that write has succeeded, and an error — never a login — otherwise
	// (ErrLoginRefusedBefore, ErrLoginOutcomeUnknown, ErrStateMissing,
	// ErrStateCorrupt, a write failure).
	Begin(ctx context.Context, c Config) error
	// Succeeded removes the intention (best effort: if it fails, the next
	// login — at the token's expiry, in the same process — is blocked,
	// fail-closed).
	Succeeded(ctx context.Context, c Config) error
	// NotSent removes the intention of a login that provably never left
	// (the connection or the TLS handshake failed before the request was
	// written): it cannot have counted against the account.
	NotSent(ctx context.Context, c Config) error
	// Refused moves the intention to refused — into an emptied Secret too
	// (a new key is made), so a reset during the login loses nothing.
	Refused(ctx context.Context, c Config) error
}

// accountMAC identifies an account by username and password, under key.
// Not the URL: Ceph counts failed logins per user, whatever address the
// login came through (a trailing slash, another host name, an IP, another
// case), so a refusal must block the same credentials at every URL.
func accountMAC(key []byte, c Config) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(c.Username + "\x00" + c.Password))
	return hex.EncodeToString(m.Sum(nil))
}

func keyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// newKey returns a fresh HMAC key.
func newKey() ([]byte, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// state is the parsed, validated Secret.
type state struct {
	key     []byte
	pending []string
	refused []string
}

func parseList(raw []byte) ([]string, error) {
	var out []string
	for l := range strings.SplitSeq(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if b, err := hex.DecodeString(l); err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("%w: an entry is not a MAC", ErrStateCorrupt)
		}
		out = append(out, l)
	}
	return out, nil
}

// parseState validates the Secret strictly. empty reports a Secret with
// none of the keys at all (the only case where a key may be generated).
func parseState(sec *corev1.Secret) (st state, empty bool, err error) {
	d := sec.Data
	key, fp := d["hmac-key"], string(d["key-fp"])
	if len(key) == 0 && fp == "" && len(strings.TrimSpace(string(d["pending"]))) == 0 && len(strings.TrimSpace(string(d["refused"]))) == 0 {
		return state{}, true, nil
	}
	if len(key) != 32 {
		return state{}, false, fmt.Errorf("%w: hmac-key is %d bytes, want 32", ErrStateCorrupt, len(key))
	}
	if !hmac.Equal([]byte(fp), []byte(keyFingerprint(key))) {
		return state{}, false, fmt.Errorf("%w: key-fp does not match hmac-key", ErrStateCorrupt)
	}
	if st.pending, err = parseList(d["pending"]); err != nil {
		return state{}, false, err
	}
	if st.refused, err = parseList(d["refused"]); err != nil {
		return state{}, false, err
	}
	st.key = key
	return st, false, nil
}

func contains(list []string, mac string) bool {
	for _, x := range list {
		if hmac.Equal([]byte(x), []byte(mac)) {
			return true
		}
	}
	return false
}

// bounded keeps the MaxRefused newest entries of refused (FIFO). pending
// is never trimmed: Begin refuses to add to a full one.
func bounded(list []string) []string {
	if len(list) > MaxRefused {
		return list[len(list)-MaxRefused:]
	}
	return list
}

// SecretLoginGate keeps the gate in StateSecretName. Reads go through a
// non-caching reader; writes are updates of the existing Secret, which the
// deployment manifests create — a missing one is reported, never created.
type SecretLoginGate struct {
	Reader    client.Reader
	Writer    client.Client
	Namespace string
}

func (g *SecretLoginGate) get(ctx context.Context) (*corev1.Secret, error) {
	var sec corev1.Secret
	err := g.Reader.Get(ctx, client.ObjectKey{Namespace: g.Namespace, Name: StateSecretName}, &sec)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s/%s, recreate it (see docs/storage-mgr-account.md)", ErrStateMissing, g.Namespace, StateSecretName)
	}
	if err != nil {
		return nil, fmt.Errorf("reading Secret %s/%s: %w", g.Namespace, StateSecretName, err)
	}
	return &sec, nil
}

// update runs one read-validate-change-write round, retried on a failed
// write (a conflict re-reads). change returns the new lists, or an error
// that stops everything (no write).
func (g *SecretLoginGate) update(ctx context.Context, generateKey bool, change func(st *state) error) error {
	var last error
	for range writeAttempts {
		sec, err := g.get(ctx)
		if err != nil {
			return err
		}
		st, empty, err := parseState(sec)
		if err != nil {
			return err
		}
		if empty {
			if !generateKey {
				return nil // nothing recorded, nothing to change
			}
			if st.key, err = newKey(); err != nil {
				return err
			}
		}
		if err := change(&st); err != nil {
			return err
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data["hmac-key"] = st.key
		sec.Data["key-fp"] = []byte(keyFingerprint(st.key))
		sec.Data["pending"] = []byte(strings.Join(st.pending, "\n"))
		sec.Data["refused"] = []byte(strings.Join(bounded(st.refused), "\n"))
		if last = g.Writer.Update(ctx, sec); last == nil {
			return nil
		}
	}
	return fmt.Errorf("writing Secret %s/%s: %w", g.Namespace, StateSecretName, last)
}

// Begin implements LoginGate.
func (g *SecretLoginGate) Begin(ctx context.Context, c Config) error {
	return g.update(ctx, true, func(st *state) error {
		mac := accountMAC(st.key, c)
		if contains(st.refused, mac) {
			return ErrLoginRefusedBefore
		}
		if contains(st.pending, mac) {
			return ErrLoginOutcomeUnknown
		}
		if len(st.pending) >= MaxRefused {
			return ErrTooManyUnresolved
		}
		st.pending = append(st.pending, mac)
		return nil
	})
}

// Succeeded implements LoginGate.
func (g *SecretLoginGate) Succeeded(ctx context.Context, c Config) error {
	return g.update(ctx, false, func(st *state) error {
		mac := accountMAC(st.key, c)
		st.pending = slices.DeleteFunc(st.pending, func(x string) bool { return x == mac })
		return nil
	})
}

// NotSent implements LoginGate.
func (g *SecretLoginGate) NotSent(ctx context.Context, c Config) error {
	return g.Succeeded(ctx, c)
}

// Refused implements LoginGate. generateKey is true: if the Secret was
// emptied between Begin and here, the refusal is written again under a new
// key rather than silently dropped.
func (g *SecretLoginGate) Refused(ctx context.Context, c Config) error {
	return g.update(ctx, true, func(st *state) error {
		mac := accountMAC(st.key, c)
		st.pending = slices.DeleteFunc(st.pending, func(x string) bool { return x == mac })
		if !contains(st.refused, mac) {
			st.refused = append(st.refused, mac)
		}
		return nil
	})
}
