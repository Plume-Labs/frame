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
	"errors"
	"fmt"
	"regexp"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/cosign/v3/pkg/cosign"
	ociremote "github.com/sigstore/cosign/v3/pkg/oci/remote"
	"github.com/sigstore/sigstore-go/pkg/root"
)

// GitHubOIDCIssuer is the issuer of the certificates the build workflow's
// keyless signatures carry.
const GitHubOIDCIssuer = "https://token.actions.githubusercontent.com"

// CosignVerifier is the real Verifier: keyless cosign signatures, accepted only
// when made by the pinned build workflow on a release tag.
type CosignVerifier struct {
	// Repo is the release artifact repository ("ghcr.io/<owner>/hearthrack-release").
	Repo string
	// GitHubRepo is "<owner>/<name>" of the repository whose build workflow
	// signs releases, such as "Plume-Labs/frame".
	GitHubRepo string
	// Issuer defaults to GitHubOIDCIssuer.
	Issuer string
	// Material is the Sigstore trust root (Fulcio, Rekor, CT log keys).
	Material root.TrustedMaterial
	// Options are passed to every registry call.
	Options []remote.Option
}

// LoadTrustedRoot reads a Sigstore trusted root from path, or, when path is
// empty, fetches the public-good one through TUF. A site without outbound
// access ships the file and names it: nothing is then fetched at run time.
func LoadTrustedRoot(path string) (root.TrustedMaterial, error) {
	if path != "" {
		return root.NewTrustedRootFromPath(path)
	}
	return root.FetchTrustedRoot()
}

// workflow is the path of the build workflow in the signing repository.
const workflow = ".github/workflows/build.yml"

// ManifestIdentity is the one signer accepted for the release manifest of a
// version: the build workflow, on that version's own tag.
func (v *CosignVerifier) ManifestIdentity(version string) string {
	return "https://github.com/" + v.GitHubRepo + "/" + workflow + "@refs/tags/v" + version
}

// ImageIdentityPattern accepts the build workflow on any release tag: an image
// is built by the run of the tag that built it, which need not be the latest.
func (v *CosignVerifier) ImageIdentityPattern() string {
	return "^" + regexp.QuoteMeta("https://github.com/"+v.GitHubRepo+"/"+workflow+"@refs/tags/v") +
		`[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
}

func (v *CosignVerifier) issuer() string {
	if v.Issuer != "" {
		return v.Issuer
	}
	return GitHubOIDCIssuer
}

// VerifyManifest checks the signature of the release artifact at digest, then
// that manifest is the very content of that artifact.
func (v *CosignVerifier) VerifyManifest(ctx context.Context, version, digest string, manifest []byte) error {
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("artifact digest %q is not a sha256 digest", digest)
	}
	ref, err := name.NewDigest(v.Repo + "@" + digest)
	if err != nil {
		return err
	}
	id := cosign.Identity{Issuer: v.issuer(), Subject: v.ManifestIdentity(version)}
	if err := v.verify(ctx, ref, id); err != nil {
		return fmt.Errorf("signature of the release manifest: %w", err)
	}
	// What was signed is the artifact at digest; read it again by that digest
	// and require it to be the bytes the caller is about to trust.
	signed, got, err := FetchManifest(ctx, ref, v.remoteOptions(ctx)...)
	if err != nil {
		return err
	}
	if got != digest {
		return fmt.Errorf("the registry served %s for %s", got, digest)
	}
	if !bytes.Equal(signed, manifest) {
		return errors.New("the release manifest is not the content of the signed artifact")
	}
	return nil
}

// VerifyImage checks the signature of one image reference pinned by digest.
func (v *CosignVerifier) VerifyImage(ctx context.Context, ref string) error {
	r, err := name.ParseReference(ref)
	if err != nil {
		return err
	}
	d, ok := r.(name.Digest)
	if !ok {
		return fmt.Errorf("image %q is not pinned by digest", ref)
	}
	id := cosign.Identity{Issuer: v.issuer(), SubjectRegExp: v.ImageIdentityPattern()}
	return v.verify(ctx, d, id)
}

func (v *CosignVerifier) remoteOptions(ctx context.Context) []remote.Option {
	return append([]remote.Option{remote.WithContext(ctx)}, v.Options...)
}

// verify accepts the signature in either of the two layouts cosign writes: a
// signature tag next to the image, or a Sigstore bundle attached to it. Both
// must name the digest in ref and the pinned identity; anything else, and any
// doubt, is an error.
func (v *CosignVerifier) verify(ctx context.Context, ref name.Digest, id cosign.Identity) error {
	if v.Material == nil {
		return errors.New("no Sigstore trust root configured")
	}
	co := &cosign.CheckOpts{
		RegistryClientOpts: []ociremote.Option{ociremote.WithRemoteOptions(v.remoteOptions(ctx)...)},
		TrustedMaterial:    v.Material,
		Identities:         []cosign.Identity{id},
		ClaimVerifier:      cosign.SimpleClaimVerifier,
	}
	_, _, legacyErr := cosign.VerifyImageSignatures(ctx, ref, co)
	if legacyErr == nil {
		return nil
	}
	bundle := *co
	bundle.NewBundleFormat = true
	bundle.ClaimVerifier = cosign.IntotoSubjectClaimVerifier
	if _, _, err := cosign.VerifyImageAttestations(ctx, ref, &bundle); err != nil {
		return fmt.Errorf("no valid signature for %s by %s: %w (signature tag), %w (bundle)",
			ref, signerOf(id), legacyErr, err)
	}
	return nil
}

func signerOf(id cosign.Identity) string {
	if id.Subject != "" {
		return id.Subject
	}
	return id.SubjectRegExp
}
