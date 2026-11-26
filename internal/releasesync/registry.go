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
	"errors"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/Plume-Labs/frame/internal/updater"
)

// ManifestMediaType is the media type of the layer that carries release.json
// in the release artifact (the build workflow pushes it with oras).
const ManifestMediaType = "application/vnd.hearthrack.release.v1+json"

// maxManifestBytes bounds what is read from a registry: a release manifest is a
// few kilobytes, so anything larger is not one.
const maxManifestBytes = 1 << 20

// OCIRegistry reads release manifests from an OCI repository (the
// hearthrack-release artifact). Reading needs no credentials when the package
// is public.
type OCIRegistry struct {
	// Repo is the repository without a tag, such as
	// "ghcr.io/plume-labs/hearthrack-release".
	Repo string
	// Options are passed to every registry call (authentication, transport).
	Options []remote.Option
}

// Versions lists the tags that are versions. The channel tags (stable, beta)
// and signature tags (sha256-...) are not versions and are left out.
func (r *OCIRegistry) Versions(ctx context.Context) ([]string, error) {
	repo, err := name.NewRepository(r.Repo)
	if err != nil {
		return nil, fmt.Errorf("repository %q: %w", r.Repo, err)
	}
	tags, err := remote.List(repo, r.opts(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("listing tags of %s: %w", r.Repo, err)
	}
	var out []string
	for _, t := range tags {
		if _, err := updater.ParseVersion(t); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// Fetch returns release.json of one version and the digest of the artifact that
// carries it.
func (r *OCIRegistry) Fetch(ctx context.Context, version string) ([]byte, string, error) {
	ref, err := name.ParseReference(r.Repo + ":" + version)
	if err != nil {
		return nil, "", fmt.Errorf("version %q: %w", version, err)
	}
	return FetchManifest(ctx, ref, r.opts(ctx)...)
}

func (r *OCIRegistry) opts(ctx context.Context) []remote.Option {
	return append([]remote.Option{remote.WithContext(ctx)}, r.Options...)
}

// FetchManifest reads the release manifest layer of the artifact ref names, and
// returns it with the digest of that artifact. Fetching by a digest reference
// proves the digest: the registry client checks every blob it reads against
// the descriptor that names it.
func FetchManifest(_ context.Context, ref name.Reference, opts ...remote.Option) ([]byte, string, error) {
	img, err := remote.Image(ref, opts...)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", ref, err)
	}
	digest, err := img.Digest()
	if err != nil {
		return nil, "", err
	}
	m, err := img.Manifest()
	if err != nil {
		return nil, "", err
	}
	var layer *v1.Descriptor
	for i := range m.Layers {
		if string(m.Layers[i].MediaType) != ManifestMediaType {
			continue
		}
		if layer != nil {
			return nil, "", errors.New("the release artifact carries more than one release manifest")
		}
		layer = &m.Layers[i]
	}
	if layer == nil {
		return nil, "", fmt.Errorf("the artifact has no %s layer", ManifestMediaType)
	}
	if layer.Size > maxManifestBytes {
		return nil, "", fmt.Errorf("the release manifest is %d bytes, over the %d limit", layer.Size, maxManifestBytes)
	}
	l, err := img.LayerByDigest(layer.Digest)
	if err != nil {
		return nil, "", err
	}
	rc, err := l.Compressed()
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxManifestBytes {
		return nil, "", errors.New("the release manifest is over the size limit")
	}
	return body, digest.String(), nil
}
