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
	"regexp"
	"strings"
	"testing"
)

func verifier() *CosignVerifier {
	return &CosignVerifier{Repo: "registry.invalid/frame/hearthrack-release", GitHubRepo: "Plume-Labs/frame"}
}

func TestManifestIdentityIsThatVersionsOwnTag(t *testing.T) {
	got := verifier().ManifestIdentity("1.2.3-beta.1")
	want := "https://github.com/Plume-Labs/frame/.github/workflows/build.yml@refs/tags/v1.2.3-beta.1"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestImageIdentityPattern(t *testing.T) {
	re := regexp.MustCompile(verifier().ImageIdentityPattern())
	base := "https://github.com/Plume-Labs/frame/.github/workflows/build.yml@refs/"
	for subject, want := range map[string]bool{
		base + "tags/v1.0.0":        true,
		base + "tags/v0.9.0-beta.1": true,
		base + "heads/main":         false, // main images are signed, but are not releases
		base + "tags/v1.0":          false,
		base + "tags/v1.0.0/extra":  false,
		base + "tags/v1.0.0 ":       false,
		base + "tags/1.0.0":         false,
		"https://github.com/Plume-Labs/frame-evil/.github/workflows/build.yml@refs/tags/v1.0.0":  false,
		"https://github.com/Plume-Labs/frameXevil/.github/workflows/build.yml@refs/tags/v1.0.0":  false,
		"https://github.com/other/Plume-Labs/frame/.github/workflows/build.yml@refs/tags/v1.0.0": false,
		"https://github.com/Plume-Labs/frame/.github/workflows/other.yml@refs/tags/v1.0.0":       false,
		"x" + base + "tags/v1.0.0": false,
		"https://github.com/Plume-Labs/frame/.github/workflows/build.yml@refs/tags/v1.0.0\nattacker": false,
	} {
		if re.MatchString(subject) != want {
			t.Errorf("%q: matched=%v, want %v", subject, !want, want)
		}
	}
}

func TestVerifierFailsClosed(t *testing.T) {
	ctx := context.Background()
	v := verifier()

	if err := v.VerifyImage(ctx, "registry.invalid/img:latest"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("a tag was accepted: %v", err)
	}
	digest := "registry.invalid/img@sha256:" + strings.Repeat("a", 64)
	if err := v.VerifyImage(ctx, digest); err == nil || !strings.Contains(err.Error(), "trust root") {
		t.Errorf("no trust root must refuse, got %v", err)
	}
	if err := v.VerifyManifest(ctx, "1.0.0", "sha256:abc", []byte("{}")); err == nil {
		t.Error("a short digest was accepted")
	}
	if err := v.VerifyManifest(ctx, "1.0.0", "sha256:"+strings.Repeat("a", 64), []byte("{}")); err == nil {
		t.Error("no trust root must refuse the manifest too")
	}
}
