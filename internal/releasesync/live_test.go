//go:build live

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

package releasesync

import (
	"context"
	"os"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/sigstore/cosign/v3/pkg/cosign"
)

// The live test checks the verifier against a real signature on a public
// registry and the public-good Sigstore trust root, which a unit test cannot:
//
//	LIVE_REF=ghcr.io/org/image@sha256:... LIVE_SUBJECT_RE='^https://github.com/org/repo/\.github/workflows/x\.yml@refs/tags/v.*$' \
//	  go test -tags live -run TestLive ./internal/releasesync
//
// It is not part of `go test ./...`: it needs the network.
func TestLive(t *testing.T) {
	ref, re := os.Getenv("LIVE_REF"), os.Getenv("LIVE_SUBJECT_RE")
	if ref == "" || re == "" {
		t.Skip("LIVE_REF and LIVE_SUBJECT_RE not set")
	}
	material, err := LoadTrustedRoot(os.Getenv("LIVE_TRUSTED_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.NewDigest(ref)
	if err != nil {
		t.Fatal(err)
	}
	v := &CosignVerifier{Material: material}
	if err := v.verify(context.Background(), r, cosign.Identity{Issuer: GitHubOIDCIssuer, SubjectRegExp: re}); err != nil {
		t.Fatalf("the right identity was refused: %v", err)
	}
	if err := v.verify(context.Background(), r, cosign.Identity{Issuer: GitHubOIDCIssuer, SubjectRegExp: "^https://github.com/someone/else/.*$"}); err == nil {
		t.Fatal("a wrong identity was accepted")
	}
	if err := v.verify(context.Background(), r, cosign.Identity{Issuer: "https://example.com", SubjectRegExp: re}); err == nil {
		t.Fatal("a wrong issuer was accepted")
	}
}
