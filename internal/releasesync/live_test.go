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
)

// The live test checks the verifier against a real signature on a public
// registry and the public-good Sigstore trust root, which a unit test cannot:
//
//	LIVE_REF=ghcr.io/org/image@sha256:... LIVE_SUBJECT_RE='^https://github.com/org/repo/.*$' \
//	  [LIVE_ISSUER=...] [LIVE_REPOSITORY=org/repo] [LIVE_TRIGGER=push] \
//	  go test -tags live -run TestLive ./internal/releasesync
//
// It is not part of `go test ./...`: it needs the network.
func TestLive(t *testing.T) {
	ref, re := os.Getenv("LIVE_REF"), os.Getenv("LIVE_SUBJECT_RE")
	if ref == "" || re == "" {
		t.Skip("LIVE_REF and LIVE_SUBJECT_RE not set")
	}
	issuer := os.Getenv("LIVE_ISSUER")
	if issuer == "" {
		issuer = GitHubOIDCIssuer
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
	ctx := context.Background()
	good := signer{issuer: issuer, sanRegexp: re, repository: os.Getenv("LIVE_REPOSITORY"), trigger: os.Getenv("LIVE_TRIGGER")}
	if err := v.verify(ctx, r, good); err != nil {
		t.Fatalf("the right identity was refused: %v", err)
	}
	for name, who := range map[string]signer{
		"another subject":    {issuer: issuer, sanRegexp: "^https://github.com/someone/else/.*$"},
		"another issuer":     {issuer: "https://example.com", sanRegexp: re},
		"another repository": {issuer: issuer, sanRegexp: re, repository: "someone/else"},
		"another trigger":    {issuer: issuer, sanRegexp: re, trigger: "workflow_dispatch-not"},
	} {
		if err := v.verify(ctx, r, who); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
