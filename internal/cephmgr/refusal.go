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
	"fmt"

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
//   - refused:  HMAC-SHA256(hmac-key, url \x00 username \x00 password) of
//     the refused account, hex.
//
// The HMAC (rather than a plain hash) means that even a leaked Secret does
// not let anyone test password guesses offline without also knowing the
// key — which is in the same Secret, but a plain hash would need nothing.

// StateSecretName is the dedicated Secret, in the manager's namespace. The
// deployment manifests create it empty, so the manager needs only get and
// update on this one name (config/manager/ceph_dashboard_state.yaml,
// charts/frame/templates/ceph-dashboard-state.yaml).
const StateSecretName = "frame-ceph-dashboard-state"

// RefusalState is what the store holds.
type RefusalState struct {
	Key     []byte
	Refused string // hex HMAC of the refused account; "" when none
}

// RefusalStore persists the refusal.
type RefusalStore interface {
	Load(ctx context.Context) (RefusalState, error)
	Save(ctx context.Context, s RefusalState) error
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

// Load implements RefusalStore. A missing Secret is an empty state.
func (s *SecretRefusalStore) Load(ctx context.Context) (RefusalState, error) {
	var sec corev1.Secret
	err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: StateSecretName}, &sec)
	if apierrors.IsNotFound(err) {
		return RefusalState{}, nil
	}
	if err != nil {
		return RefusalState{}, fmt.Errorf("reading Secret %s/%s: %w", s.Namespace, StateSecretName, err)
	}
	return RefusalState{Key: sec.Data["hmac-key"], Refused: string(sec.Data["refused"])}, nil
}

// Save implements RefusalStore.
func (s *SecretRefusalStore) Save(ctx context.Context, st RefusalState) error {
	var sec corev1.Secret
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: StateSecretName}, &sec); err != nil {
		return fmt.Errorf("reading Secret %s/%s: %w", s.Namespace, StateSecretName, err)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data["hmac-key"] = st.Key
	sec.Data["refused"] = []byte(st.Refused)
	if err := s.Writer.Update(ctx, &sec); err != nil {
		return fmt.Errorf("writing Secret %s/%s: %w", s.Namespace, StateSecretName, err)
	}
	return nil
}
