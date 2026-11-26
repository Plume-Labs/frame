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
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// push writes an artifact whose layers are the given (media type, bytes) pairs
// and tags it, the way the build workflow pushes release.json with oras.
func push(t *testing.T, repo, tag string, layers ...[2]string) {
	t.Helper()
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	for _, l := range layers {
		var err error
		img, err = mutate.Append(img, mutate.Addendum{Layer: static.NewLayer([]byte(l[1]), types.MediaType(l[0]))})
		if err != nil {
			t.Fatal(err)
		}
	}
	ref, err := name.ParseReference(repo + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://") + "/frame/hearthrack-release"
}

func TestOCIRegistryListsOnlyVersions(t *testing.T) {
	repo := newRepo(t)
	push(t, repo, "1.0.0", [2]string{ManifestMediaType, "{}"})
	push(t, repo, "1.1.0-beta.1", [2]string{ManifestMediaType, "{}"})
	push(t, repo, "stable", [2]string{ManifestMediaType, "{}"})
	push(t, repo, "beta", [2]string{ManifestMediaType, "{}"})
	push(t, repo, "sha256-"+strings.Repeat("a", 64)+".sig", [2]string{ManifestMediaType, "{}"})

	got, err := (&OCIRegistry{Repo: repo}).Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want the two version tags, got %v", got)
	}
	for _, v := range got {
		if v != "1.0.0" && v != "1.1.0-beta.1" {
			t.Errorf("%q is not a version", v)
		}
	}
}

func TestOCIRegistryFetch(t *testing.T) {
	repo := newRepo(t)
	body := `{"schema":1,"version":"1.0.0"}`
	push(t, repo, "1.0.0", [2]string{ManifestMediaType, body})

	r := &OCIRegistry{Repo: repo}
	got, digest, err := r.Fetch(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(body)) {
		t.Fatalf("manifest = %q", got)
	}
	if !digestPattern.MatchString(digest) {
		t.Fatalf("digest %q is not sha256", digest)
	}

	// The same artifact read by its digest is the same bytes: that is how the
	// verifier re-reads what was signed.
	ref, err := name.NewDigest(repo + "@" + digest)
	if err != nil {
		t.Fatal(err)
	}
	again, d2, err := FetchManifest(context.Background(), ref)
	if err != nil || d2 != digest || !bytes.Equal(again, got) {
		t.Fatalf("by digest: %q %s %v", again, d2, err)
	}
}

func TestOCIRegistryFetchRefusesWhatIsNotAReleaseManifest(t *testing.T) {
	repo := newRepo(t)
	push(t, repo, "none", [2]string{"text/plain", "x"})
	push(t, repo, "two", [2]string{ManifestMediaType, "a"}, [2]string{ManifestMediaType, "b"})
	push(t, repo, "big", [2]string{ManifestMediaType, strings.Repeat("x", maxManifestBytes+1)})

	r := &OCIRegistry{Repo: repo}
	for _, tag := range []string{"none", "two", "big"} {
		// Tags that are not versions are not listed, but Fetch takes any tag;
		// what it must refuse is the content.
		if _, _, err := r.Fetch(context.Background(), tag); err == nil {
			t.Errorf("%s: accepted", tag)
		}
	}
	if _, _, err := r.Fetch(context.Background(), "9.9.9"); err == nil {
		t.Error("a missing version was accepted")
	}
}
