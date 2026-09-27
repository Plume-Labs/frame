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

	st, err := s.Load(ctx)
	if err != nil || st.Refused != "" || len(st.Key) != 0 {
		t.Fatalf("empty Secret: %+v, %v", st, err)
	}
	if err := s.Save(ctx, RefusalState{Key: []byte("k"), Refused: "mac"}); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	_ = c.Get(ctx, client.ObjectKey{Namespace: "frame-system", Name: StateSecretName}, &sec)
	if string(sec.Data["refused"]) != "mac" || string(sec.Data["hmac-key"]) != "k" {
		t.Fatalf("written: %v", sec.Data)
	}
	if st, _ := s.Load(ctx); st.Refused != "mac" {
		t.Fatalf("read back: %+v", st)
	}

	// No Secret: an empty state to read, and a write that fails (the
	// manager has no create right; the reader then stays fail-closed).
	none := fake.NewClientBuilder().Build()
	s2 := &SecretRefusalStore{Reader: none, Writer: none, Namespace: "frame-system"}
	if st, err := s2.Load(ctx); err != nil || st.Refused != "" {
		t.Fatalf("missing Secret load: %+v, %v", st, err)
	}
	if err := s2.Save(ctx, RefusalState{Key: []byte("k"), Refused: "mac"}); err == nil {
		t.Fatal("saving into a missing Secret succeeded")
	}
}
