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

// Package releasesync turns signed release manifests found in a registry into
// FrameRelease objects, and is the only code that may say a release is
// verified. The updater (internal/updater) reads that word and never writes it:
// the two run under different ServiceAccounts, so a compromised updater cannot
// declare a release trustworthy, and a compromised syncer cannot move a
// component.
//
// What the syncer trusts: nothing it reads from the registry until the
// signature on the manifest, and on every image the manifest names, has been
// checked against the pinned identity (Verifier). What it never does: change
// the content of a release it has already recorded. A version is immutable; if
// the registry later serves different bytes under the same version, the release
// is marked unverified and left for a person to look at.
package releasesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/updater"
)

// Registry is where signed release manifests live.
type Registry interface {
	// Versions lists the release versions published (the version tags of the
	// release artifact), in any order.
	Versions(ctx context.Context) ([]string, error)
	// Fetch returns the manifest bytes of one version and the digest
	// (sha256:...) of the artifact that carries them.
	Fetch(ctx context.Context, version string) (manifest []byte, digest string, err error)
}

// Verifier checks signatures against the pinned identity. The real one uses
// cosign; tests use a fake. Both must fail closed: any doubt is an error.
type Verifier interface {
	// VerifyManifest checks the signature of the release artifact at digest AND
	// that manifest (the bytes the syncer is about to trust) is exactly the
	// content that signed artifact carries: a signature on an artifact says
	// nothing about bytes that merely arrived next to its digest.
	VerifyManifest(ctx context.Context, version, digest string, manifest []byte) error
	// VerifyImage checks the signature of one image reference (pinned by digest).
	VerifyImage(ctx context.Context, ref string) error
}

// Syncer records releases. One Sync is one pass; the caller repeats it.
type Syncer struct {
	Client    client.Client
	Namespace string
	Registry  Registry
	Verifier  Verifier
	// Maturation is how long a release must have been published before it is
	// offered: a short window in which a compromised channel can be noticed
	// and the release withdrawn. 0 offers it at once (a site that wants every
	// release immediately, such as the project's own cluster).
	Maturation time.Duration
	Now        func() time.Time
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Result says what one pass did, for logs and tests.
type Result struct {
	Created  []string
	Verified []string
	Waiting  []string // verified signatures, still maturing
	Rejected map[string]string
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Labels and annotations the syncer owns.
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "frame-release-sync"
	// QuarantineAnnotation, once set on a release, stops the syncer from ever
	// verifying it again. Only a person removes it.
	QuarantineAnnotation = "frame.plume-labs.io/quarantined"
	// maxFuturePublication bounds how far ahead a signed publishedAt may lie.
	maxFuturePublication = 24 * time.Hour
)

var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

// ObjectName is the FrameRelease name for a version.
func ObjectName(version string) string {
	return "release-" + strings.Trim(nonName.ReplaceAllString(strings.ToLower(version), "-"), "-")
}

// Sync runs one pass over every version the registry lists. A version that
// fails does not stop the others; its reason is in Result.Rejected.
func (s *Syncer) Sync(ctx context.Context) (Result, error) {
	res := Result{Rejected: map[string]string{}}
	versions, err := s.Registry.Versions(ctx)
	if err != nil {
		return res, fmt.Errorf("listing releases: %w", err)
	}
	sort.Strings(versions)
	for _, v := range versions {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if _, err := updater.ParseVersion(v); err != nil {
			res.Rejected[v] = "not a version: " + err.Error()
			continue
		}
		if err := s.one(ctx, v, &res); err != nil {
			res.Rejected[v] = err.Error()
		}
	}
	return res, nil
}

func (s *Syncer) one(ctx context.Context, version string, res *Result) error {
	key := client.ObjectKey{Namespace: s.Namespace, Name: ObjectName(version)}
	var cur framev1beta1.FrameRelease
	err := s.Client.Get(ctx, key, &cur)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if exists && cur.Spec.Version != version {
		return fmt.Errorf("object %s already holds version %s", key.Name, cur.Spec.Version)
	}
	if exists && cur.Labels[managedByLabel] != managedByValue {
		return fmt.Errorf("object %s was not created by the release syncer: refusing to adopt it", key.Name)
	}
	if exists && cur.Annotations[QuarantineAnnotation] != "" {
		return fmt.Errorf("release %s is quarantined (%s): remove the annotation after looking at it", version, cur.Annotations[QuarantineAnnotation])
	}

	raw, digest, err := s.Registry.Fetch(ctx, version)
	if err != nil {
		return fmt.Errorf("fetching: %w", err)
	}
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("the registry gave %q as the artifact digest", digest)
	}
	m, err := updater.ParseManifest(raw)
	if err != nil {
		return err
	}
	if m.Version != version {
		return fmt.Errorf("the artifact tagged %s says it is %s", version, m.Version)
	}
	if publishedAt(m).IsZero() {
		// Without a date the maturation window cannot be honoured: refuse
		// rather than guess one.
		return errors.New("the manifest has no valid publishedAt")
	}
	if publishedAt(m).After(s.now().Add(maxFuturePublication)) {
		return errors.New("the manifest is dated too far in the future")
	}
	want := specFrom(m)

	if exists {
		// A recorded release never changes. Different content under the same
		// version is the signal worth a person's attention, not something to
		// follow silently.
		if !reflect.DeepEqual(comparable(cur.Spec), comparable(want)) || !samePublication(cur.Spec.PublishedAt, want.PublishedAt) {
			return s.markUnverified(ctx, &cur, "the registry now serves different content for this version: not trusted")
		}
		if cur.Status.ManifestDigest != "" && cur.Status.ManifestDigest != digest {
			return s.markUnverified(ctx, &cur, "the artifact digest of this version changed: not trusted")
		}
		// Already settled and unchanged: nothing more to prove. The fetch above
		// is what notices a registry that starts serving something else.
		if cur.Status.Verified && cur.Status.ManifestDigest == digest {
			return nil
		}
	}

	if err := s.Verifier.VerifyManifest(ctx, version, digest, raw); err != nil {
		return fmt.Errorf("manifest signature: %w", err)
	}
	for _, name := range sortedKeys(m.Images) {
		if err := s.Verifier.VerifyImage(ctx, m.Images[name]); err != nil {
			return fmt.Errorf("image %s signature: %w", name, err)
		}
	}

	if !exists {
		cur = framev1beta1.FrameRelease{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: s.Namespace, Name: key.Name,
				Labels:            map[string]string{managedByLabel: managedByValue},
				CreationTimestamp: metav1.NewTime(s.now()), // the API server sets its own; this is for a client that does not
			},
			Spec: want,
		}
		if err := s.Client.Create(ctx, &cur); err != nil {
			return err
		}
		res.Created = append(res.Created, version)
	}

	now := metav1.NewTime(s.now())
	cur.Status.ManifestDigest = digest
	// The window runs from the later of the signed date and the moment this
	// cluster first saw the release. publishedAt is chosen by whoever signs, so
	// on its own it would let a compromised build date its release a month ago
	// and skip the very delay meant to catch that.
	firstSeen := cur.CreationTimestamp.Time
	if firstSeen.IsZero() {
		firstSeen = s.now()
	}
	matureAt := later(publishedAt(m), firstSeen).Add(s.Maturation)
	if s.now().Before(matureAt) {
		cur.Status.Verified = false
		cur.Status.Message = "signature verified; offered from " + matureAt.UTC().Format(time.RFC3339)
		res.Waiting = append(res.Waiting, version)
	} else {
		cur.Status.Verified = true
		cur.Status.VerifiedAt = &now
		cur.Status.Message = ""
		res.Verified = append(res.Verified, version)
	}
	return s.Client.Status().Update(ctx, &cur)
}

// markUnverified takes a release back and quarantines it: the syncer will not
// verify it again, even if the registry goes back to the original content,
// until a person has looked and removed the annotation.
func (s *Syncer) markUnverified(ctx context.Context, r *framev1beta1.FrameRelease, msg string) error {
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	r.Annotations[QuarantineAnnotation] = msg
	if err := s.Client.Update(ctx, r); err != nil {
		return err
	}
	r.Status.Verified = false
	r.Status.VerifiedAt = nil
	r.Status.Message = msg
	if err := s.Client.Status().Update(ctx, r); err != nil {
		return err
	}
	return errors.New(msg)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func samePublication(a, b *metav1.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Time.UTC().Truncate(time.Second).Equal(b.Time.UTC().Truncate(time.Second))
}

// specFrom copies a parsed manifest into the FrameRelease spec. Notes and
// Revoked are not in the manifest: a release enters unrevoked and without notes.
func specFrom(m updater.Manifest) framev1beta1.FrameReleaseSpec {
	sp := framev1beta1.FrameReleaseSpec{Version: m.Version, Channel: m.Channel, Images: m.Images}
	if m.MinFrom != nil {
		sp.MinFrom = *m.MinFrom
	}
	if m.RollbackSafeTo != nil {
		sp.RollbackSafeTo = *m.RollbackSafeTo
	}
	if t := publishedAt(m); !t.IsZero() {
		mt := metav1.NewTime(t)
		sp.PublishedAt = &mt
	}
	return sp
}

// publishedAt reads the manifest's timestamp; zero when absent or malformed.
func publishedAt(m updater.Manifest) time.Time {
	t, err := time.Parse(time.RFC3339, m.PublishedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// comparable drops what the syncer does not own or that an operator may set
// afterwards (notes, revocation), so only the signed content is compared.
func comparable(sp framev1beta1.FrameReleaseSpec) map[string]any {
	b, _ := json.Marshal(struct {
		Version, Channel, MinFrom, RollbackSafeTo string
		Images                                    map[string]string
	}{sp.Version, sp.Channel, sp.MinFrom, sp.RollbackSafeTo, sp.Images})
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
