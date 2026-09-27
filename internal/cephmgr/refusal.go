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

// Where the refusal of an account lives. Ceph counts failed dashboard
// logins per user and only a successful login resets the count, so a
// refusal must outlive the process that met it — a manager restarting in a
// loop must not add one failure per restart. It is kept in a dedicated
// Secret, not in FrameStorage status: status is readable by every Frame
// viewer, a Secret is not.
//
// The Secret holds two keys:
//   - hmac-key: 32 random bytes, generated the first time a refusal is
//     recorded;
//   - refused:  one HMAC-SHA256(hmac-key, url \x00 username \x00 password)
//     per refused account, hex, one per line, the MaxRefused newest.
//
// The HMAC (rather than a plain hash) means that even a leaked Secret does
// not let anyone test password guesses offline without also knowing the
// key — which is in the same Secret, but a plain hash would need nothing.

// StateSecretName is the dedicated Secret, in the manager's namespace. The
// deployment manifests create it empty, so the manager needs only get and
// update on this one name (config/manager/ceph_dashboard_state.yaml,
// charts/frame/templates/ceph-dashboard-state.yaml).
const StateSecretName = "frame-ceph-dashboard-state"

// MaxRefused bounds the refused accounts kept, oldest dropped first: A
// refused, then B, then back to A must still find A refused.
const MaxRefused = 16

// ErrStateMissing: the state Secret does not exist. It is created by the
// deployment manifests and never by the manager (get/update only), so no
// login is attempted — a refusal could not be recorded, and every restart
// would add a failure to Ceph's counter.
var ErrStateMissing = errors.New("state Secret missing")

// RefusalStore persists refusals.
type RefusalStore interface {
	// IsRefused reports whether this account was refused.
	IsRefused(ctx context.Context, c Config) (bool, error)
	// Record adds this account to the refused ones.
	Record(ctx context.Context, c Config) error
}

// accountMAC identifies an account by content, under key.
func accountMAC(key []byte, c Config) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(c.BaseURL + "\x00" + c.Username + "\x00" + c.Password))
	return hex.EncodeToString(m.Sum(nil))
}

// newKey returns a fresh HMAC key.
func newKey() ([]byte, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// SecretRefusalStore keeps the refusal in StateSecretName. Reads go
// through a non-caching reader (the manager must not watch every Secret);
// writes are updates of the existing Secret — it is created by the
// deployment manifests, and a missing one is reported, not created.
type SecretRefusalStore struct {
	Reader    client.Reader
	Writer    client.Client
	Namespace string
}

func (s *SecretRefusalStore) get(ctx context.Context) (*corev1.Secret, error) {
	var sec corev1.Secret
	err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: StateSecretName}, &sec)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s/%s, recreate it (see docs/storage-mgr-account.md)", ErrStateMissing, s.Namespace, StateSecretName)
	}
	if err != nil {
		return nil, fmt.Errorf("reading Secret %s/%s: %w", s.Namespace, StateSecretName, err)
	}
	return &sec, nil
}

// refusedList is the stored MACs, oldest first (one per line).
func refusedList(sec *corev1.Secret) []string {
	var out []string
	for _, l := range strings.Split(string(sec.Data["refused"]), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// IsRefused implements RefusalStore.
func (s *SecretRefusalStore) IsRefused(ctx context.Context, c Config) (bool, error) {
	sec, err := s.get(ctx)
	if err != nil {
		return false, err
	}
	key := sec.Data["hmac-key"]
	if len(key) == 0 {
		return false, nil
	}
	mac := accountMAC(key, c)
	for _, r := range refusedList(sec) {
		if hmac.Equal([]byte(r), []byte(mac)) {
			return true, nil
		}
	}
	return false, nil
}

// Record implements RefusalStore. The key is the one in the Secret at the
// time of this Get/Update (generated if there is none yet), never one read
// earlier; the list keeps the MaxRefused newest.
func (s *SecretRefusalStore) Record(ctx context.Context, c Config) error {
	sec, err := s.get(ctx)
	if err != nil {
		return err
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	key := sec.Data["hmac-key"]
	list := refusedList(sec)
	if len(key) == 0 {
		if key, err = newKey(); err != nil {
			return err
		}
		list = nil // MACs under another key (or none) mean nothing now
	}
	mac := accountMAC(key, c)
	if !slices.Contains(list, mac) {
		list = append(list, mac)
	}
	if len(list) > MaxRefused {
		list = list[len(list)-MaxRefused:]
	}
	sec.Data["hmac-key"] = key
	sec.Data["refused"] = []byte(strings.Join(list, "\n"))
	if err := s.Writer.Update(ctx, sec); err != nil {
		return fmt.Errorf("writing Secret %s/%s: %w", s.Namespace, StateSecretName, err)
	}
	return nil
}
