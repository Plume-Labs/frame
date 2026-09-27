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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSecretRefusalStore(t *testing.T) {
	ctx := context.Background()
	empty := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: StateSecretName, Namespace: "frame-system"}}
	c := fake.NewClientBuilder().WithObjects(empty).Build()
	s := &SecretRefusalStore{Reader: c, Writer: c, Namespace: "frame-system"}
	acct := Config{BaseURL: "https://d:8443", Username: "u", Password: "p"}

	if refused, err := s.IsRefused(ctx, acct); err != nil || refused {
		t.Fatalf("empty Secret: refused %v, %v", refused, err)
	}
	if err := s.Record(ctx, acct); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	_ = c.Get(ctx, client.ObjectKey{Namespace: "frame-system", Name: StateSecretName}, &sec)
	if len(sec.Data["hmac-key"]) != 32 || string(sec.Data["refused"]) != accountMAC(sec.Data["hmac-key"], acct) {
		t.Fatalf("written: %v", sec.Data)
	}
	if refused, _ := s.IsRefused(ctx, acct); !refused {
		t.Fatal("recorded account not read back as refused")
	}

	// No Secret: an error, never an empty state — no login may follow
	// (a refusal could not be recorded), and no write is attempted.
	none := fake.NewClientBuilder().Build()
	s2 := &SecretRefusalStore{Reader: none, Writer: none, Namespace: "frame-system"}
	if _, err := s2.IsRefused(ctx, acct); !errors.Is(err, ErrStateMissing) || !strings.Contains(err.Error(), "frame-system/frame-ceph-dashboard-state") {
		t.Fatalf("missing Secret: err %v, want ErrStateMissing naming it", err)
	}
	if err := s2.Record(ctx, acct); !errors.Is(err, ErrStateMissing) {
		t.Fatalf("record into a missing Secret: err %v", err)
	}
}
