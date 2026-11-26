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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

const ns = "frame-system"

var published = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func manifest(version string, uiDigest string) []byte {
	b, _ := json.Marshal(map[string]any{
		"schema": 1, "version": version, "channel": "stable", "publishedAt": published.Format(time.RFC3339),
		"images":         map[string]string{"ui": "ghcr.io/plume-labs/hearthrack-ui@" + uiDigest},
		"minFrom":        nil,
		"rollbackSafeTo": nil,
	})
	return b
}

type fakeRegistry struct {
	versions []string
	bodies   map[string][]byte
	digests  map[string]string
}

func (f *fakeRegistry) Versions(context.Context) ([]string, error) { return f.versions, nil }
func (f *fakeRegistry) Fetch(_ context.Context, v string) ([]byte, string, error) {
	b, ok := f.bodies[v]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return b, f.digests[v], nil
}

type fakeVerifier struct {
	badManifest map[string]bool
	badImage    map[string]bool
	// signed, when set, is the content each signed artifact digest really
	// carries; the verifier refuses bytes that differ from it.
	signed map[string][]byte
	calls  int
}

func (f *fakeVerifier) VerifyManifest(_ context.Context, v, digest string, manifest []byte) error {
	f.calls++
	if f.badManifest[v] {
		return errors.New("no valid signature")
	}
	if f.signed != nil && !bytes.Equal(f.signed[digest], manifest) {
		return errors.New("the bytes are not the content of the signed artifact")
	}
	return nil
}
func (f *fakeVerifier) VerifyImage(_ context.Context, ref string) error {
	f.calls++
	if f.badImage[ref] {
		return errors.New("image not signed by the pinned identity")
	}
	return nil
}

func setup(t *testing.T, reg *fakeRegistry, ver *fakeVerifier, maturation time.Duration, now time.Time) (*Syncer, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	_ = framev1beta1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1beta1.FrameRelease{}).Build()
	return &Syncer{Client: c, Namespace: ns, Registry: reg, Verifier: ver, Maturation: maturation, Now: func() time.Time { return now }}, c
}

func get(t *testing.T, c client.Client, version string) framev1beta1.FrameRelease {
	t.Helper()
	var r framev1beta1.FrameRelease
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: ObjectName(version)}, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func oneRelease(v string) *fakeRegistry {
	return &fakeRegistry{versions: []string{v}, bodies: map[string][]byte{v: manifest(v, digest("a"))}, digests: map[string]string{v: digest("1")}}
}

func TestSyncRecordsAVerifiedRelease(t *testing.T) {
	s, c := setup(t, oneRelease("1.1.0"), &fakeVerifier{}, 0, published.Add(time.Hour))
	res, err := s.Sync(context.Background())
	if err != nil || len(res.Verified) != 1 || len(res.Rejected) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	r := get(t, c, "1.1.0")
	if !r.Status.Verified || r.Status.ManifestDigest != digest("1") || r.Spec.Images["ui"] == "" {
		t.Fatalf("%+v", r)
	}
}

func TestSyncRefusesWhatIsNotSigned(t *testing.T) {
	ref := "ghcr.io/plume-labs/hearthrack-ui@" + digest("a")
	for name, ver := range map[string]*fakeVerifier{
		"manifest": {badManifest: map[string]bool{"1.1.0": true}},
		"image":    {badImage: map[string]bool{ref: true}},
	} {
		s, c := setup(t, oneRelease("1.1.0"), ver, 0, published.Add(time.Hour))
		res, _ := s.Sync(context.Background())
		if len(res.Rejected) != 1 || len(res.Verified) != 0 {
			t.Fatalf("%s: %+v", name, res)
		}
		// Nothing is recorded for content that failed its signature.
		var l framev1beta1.FrameReleaseList
		if err := c.List(context.Background(), &l); err != nil || len(l.Items) != 0 {
			t.Fatalf("%s: recorded %d", name, len(l.Items))
		}
	}
}

func TestSyncMakesAReleaseWaitOutTheMaturation(t *testing.T) {
	reg, ver := oneRelease("1.1.0"), &fakeVerifier{}
	s, c := setup(t, reg, ver, 48*time.Hour, published.Add(time.Hour))
	res, _ := s.Sync(context.Background())
	if len(res.Waiting) != 1 || get(t, c, "1.1.0").Status.Verified {
		t.Fatalf("offered before maturation: %+v", res)
	}
	s.Now = func() time.Time { return published.Add(49 * time.Hour) }
	res, _ = s.Sync(context.Background())
	if len(res.Verified) != 1 || !get(t, c, "1.1.0").Status.Verified {
		t.Fatalf("not offered after maturation: %+v", res)
	}
}

func TestSyncNeverFollowsAChangeOfContent(t *testing.T) {
	reg := oneRelease("1.1.0")
	s, c := setup(t, reg, &fakeVerifier{}, 0, published.Add(time.Hour))
	if _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The registry now serves other images under the same version.
	reg.bodies["1.1.0"] = manifest("1.1.0", digest("b"))
	res, _ := s.Sync(context.Background())
	r := get(t, c, "1.1.0")
	if r.Status.Verified || len(res.Rejected) != 1 {
		t.Fatalf("followed a change: %+v", r.Status)
	}
	if r.Spec.Images["ui"] != "ghcr.io/plume-labs/hearthrack-ui@"+digest("a") {
		t.Fatal("the recorded content was overwritten")
	}
}

func TestSyncNoticesAMovedArtifactDigest(t *testing.T) {
	reg := oneRelease("1.1.0")
	s, c := setup(t, reg, &fakeVerifier{}, 0, published.Add(time.Hour))
	_, _ = s.Sync(context.Background())
	reg.digests["1.1.0"] = digest("2") // same bytes, another artifact
	_, _ = s.Sync(context.Background())
	if get(t, c, "1.1.0").Status.Verified {
		t.Fatal("a moved artifact was kept as verified")
	}
}

func TestSyncDoesNotReverifyASettledRelease(t *testing.T) {
	ver := &fakeVerifier{}
	s, _ := setup(t, oneRelease("1.1.0"), ver, 0, published.Add(time.Hour))
	_, _ = s.Sync(context.Background())
	n := ver.calls
	_, _ = s.Sync(context.Background())
	if ver.calls != n {
		t.Fatalf("verified again: %d -> %d", n, ver.calls)
	}
}

func TestSyncRefusesAManifestWhoseVersionDiffersFromItsTag(t *testing.T) {
	reg := oneRelease("1.1.0")
	reg.bodies["1.1.0"] = manifest("9.9.9", digest("a"))
	s, _ := setup(t, reg, &fakeVerifier{}, 0, published.Add(time.Hour))
	res, _ := s.Sync(context.Background())
	if !strings.Contains(res.Rejected["1.1.0"], "says it is") {
		t.Fatalf("%+v", res)
	}
}

func TestSyncOneBadVersionDoesNotStopTheOthers(t *testing.T) {
	reg := &fakeRegistry{
		versions: []string{"1.0.0", "1.1.0", "latest"},
		bodies:   map[string][]byte{"1.0.0": manifest("1.0.0", digest("a")), "1.1.0": manifest("1.1.0", digest("b"))},
		digests:  map[string]string{"1.0.0": digest("1"), "1.1.0": digest("2")},
	}
	s, _ := setup(t, reg, &fakeVerifier{badManifest: map[string]bool{"1.0.0": true}}, 0, published.Add(time.Hour))
	res, _ := s.Sync(context.Background())
	if len(res.Verified) != 1 || res.Verified[0] != "1.1.0" || len(res.Rejected) != 2 {
		t.Fatalf("%+v", res)
	}
}

func TestObjectNameIsAValidDNSLabel(t *testing.T) {
	for v, want := range map[string]string{"1.0.0": "release-1-0-0", "0.9.0-beta.1": "release-0-9-0-beta-1", "1.0.0-RC.2": "release-1-0-0-rc-2"} {
		if got := ObjectName(v); got != want {
			t.Errorf("%s: %s", v, got)
		}
	}
}

func TestSyncRefusesBytesThatAreNotTheSignedContent(t *testing.T) {
	reg := oneRelease("1.1.0")
	// The digest is that of a genuinely signed artifact, the bytes are another
	// release's images under this version.
	ver := &fakeVerifier{signed: map[string][]byte{digest("1"): manifest("1.1.0", digest("a"))}}
	reg.bodies["1.1.0"] = manifest("1.1.0", digest("c"))
	s, c := setup(t, reg, ver, 0, published.Add(time.Hour))
	res, _ := s.Sync(context.Background())
	var l framev1beta1.FrameReleaseList
	_ = c.List(context.Background(), &l)
	if len(res.Rejected) != 1 || len(l.Items) != 0 {
		t.Fatalf("%+v, recorded %d", res, len(l.Items))
	}
}

func TestSyncDoesNotLetAnAntedatedManifestSkipTheMaturation(t *testing.T) {
	reg := oneRelease("1.1.0")
	reg.bodies["1.1.0"] = manifest("1.1.0", digest("a")) // published 6 days before "now" below
	now := published.Add(6 * 24 * time.Hour)
	s, c := setup(t, reg, &fakeVerifier{}, 48*time.Hour, now)
	res, _ := s.Sync(context.Background())
	if len(res.Waiting) != 1 || get(t, c, "1.1.0").Status.Verified {
		t.Fatalf("an old date skipped the window: %+v", res)
	}
	s.Now = func() time.Time { return now.Add(49 * time.Hour) }
	if res, _ = s.Sync(context.Background()); len(res.Verified) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestSyncRefusesADateTooFarInTheFuture(t *testing.T) {
	s, _ := setup(t, oneRelease("1.1.0"), &fakeVerifier{}, 0, published.Add(-30*24*time.Hour))
	if res, _ := s.Sync(context.Background()); len(res.Rejected) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestSyncDoesNotAdoptAnObjectItDidNotCreate(t *testing.T) {
	s, c := setup(t, oneRelease("1.1.0"), &fakeVerifier{}, 0, published.Add(time.Hour))
	pre := &framev1beta1.FrameRelease{}
	pre.Namespace, pre.Name = ns, ObjectName("1.1.0")
	pre.Spec.Version, pre.Spec.Channel, pre.Spec.Images = "1.1.0", "stable", map[string]string{"ui": "x@" + digest("a")}
	if err := c.Create(context.Background(), pre); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Sync(context.Background()); len(res.Rejected) != 1 || get(t, c, "1.1.0").Status.Verified {
		t.Fatalf("adopted a foreign object: %+v", res)
	}
}

func TestSyncQuarantineSticksEvenIfTheRegistryGoesBack(t *testing.T) {
	reg := oneRelease("1.1.0")
	s, c := setup(t, reg, &fakeVerifier{}, 0, published.Add(time.Hour))
	_, _ = s.Sync(context.Background())
	good := reg.bodies["1.1.0"]
	reg.bodies["1.1.0"] = manifest("1.1.0", digest("b"))
	_, _ = s.Sync(context.Background())
	reg.bodies["1.1.0"] = good // back to the original
	_, _ = s.Sync(context.Background())
	r := get(t, c, "1.1.0")
	if r.Status.Verified || r.Annotations[QuarantineAnnotation] == "" {
		t.Fatalf("a quarantined release came back by itself: %+v", r.Status)
	}
}

func TestSyncRefusesAMalformedArtifactDigest(t *testing.T) {
	reg := oneRelease("1.1.0")
	reg.digests["1.1.0"] = "sha256:short"
	s, _ := setup(t, reg, &fakeVerifier{}, 0, published.Add(time.Hour))
	if res, _ := s.Sync(context.Background()); len(res.Rejected) != 1 {
		t.Fatalf("%+v", res)
	}
}
