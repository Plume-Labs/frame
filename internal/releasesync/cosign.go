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
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// GitHubOIDCIssuer is the issuer of the certificates the build workflow's
// keyless signatures carry.
const GitHubOIDCIssuer = "https://token.actions.githubusercontent.com"

const (
	// bundleArtifactType is the artifact type of the Sigstore bundle that
	// `cosign sign` (v3 and later) attaches to a signed digest as an OCI
	// referrer.
	bundleArtifactType = "application/vnd.dev.sigstore.bundle.v0.3+json"
	// bundleMediaTypePrefix matches the layer that carries the bundle, whatever
	// version of the bundle format it is.
	bundleMediaTypePrefix = "application/vnd.dev.sigstore.bundle"
	// signPredicateType is the predicate of the in-toto statement `cosign sign`
	// signs: a statement about the image digest and nothing else.
	signPredicateType = "https://sigstore.dev/cosign/sign/v1"
	// maxBundles bounds how many signatures of one digest are tried.
	maxBundles = 16
)

// CosignVerifier is the real Verifier: keyless Sigstore signatures, accepted
// only when made by the pinned build workflow on a release tag, in the layout
// `cosign sign` writes (a bundle attached to the signed digest).
//
// It uses sigstore-go directly, not the cosign command's library: the signature
// check is the same, and it keeps the OpenPGP code that library carries for
// key-based verification out of a binary that only ever checks keyless ones.
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

// signer is who a signature must come from.
type signer struct {
	issuer string
	// san is an exact subject alternative name, or sanRegexp a pattern; one of
	// the two is set.
	san, sanRegexp string
	// repository and trigger are the GitHub certificate extensions the Fulcio
	// certificate must carry: the SAN names a workflow file at a ref, and a
	// workflow that another repository can call would carry the same SAN, so
	// the repository that ran it is pinned too. Empty means unconstrained.
	repository, trigger string
}

func (v *CosignVerifier) signerFor(san, sanRegexp string) signer {
	return signer{issuer: v.issuer(), san: san, sanRegexp: sanRegexp, repository: v.GitHubRepo, trigger: "push"}
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
	if err := v.verify(ctx, ref, v.signerFor(v.ManifestIdentity(version), "")); err != nil {
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
	return v.verify(ctx, d, v.signerFor("", v.ImageIdentityPattern()))
}

func (v *CosignVerifier) remoteOptions(ctx context.Context) []remote.Option {
	return append([]remote.Option{remote.WithContext(ctx)}, v.Options...)
}

// verify accepts ref when one of the Sigstore bundles attached to it was made
// by the signer, is bound to exactly this digest, and carries the transparency
// log entry, certificate timestamp and trusted timestamp the trust root can
// vouch for. Anything else, and any doubt, is an error.
func (v *CosignVerifier) verify(ctx context.Context, ref name.Digest, who signer) error {
	if v.Material == nil {
		return errors.New("no Sigstore trust root configured")
	}
	algo, hexDigest, ok := strings.Cut(ref.DigestStr(), ":")
	if !ok || algo != "sha256" {
		return fmt.Errorf("digest %q is not sha256", ref.DigestStr())
	}
	sum, err := hex.DecodeString(hexDigest)
	if err != nil || len(sum) != 32 {
		return fmt.Errorf("digest %q is malformed", ref.DigestStr())
	}
	verifier, err := verify.NewVerifier(v.Material,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return err
	}
	policy, err := who.policy(sum)
	if err != nil {
		return err
	}

	bundles, err := v.bundlesOf(ctx, ref)
	if err != nil {
		return err
	}
	var failures []error
	for _, b := range bundles {
		res, err := verifier.Verify(b, policy)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// `cosign sign` signs a statement about the digest and nothing else;
		// the same identity may also sign other kinds of attestation, which
		// say nothing about whether this digest is a release.
		if res.Statement == nil || res.Statement.PredicateType != signPredicateType {
			failures = append(failures, errors.New("the signed statement is not a cosign signature of the digest"))
			continue
		}
		return nil
	}
	return fmt.Errorf("no valid signature for %s by %s: %w", ref, who.describe(), errors.Join(failures...))
}

func (s signer) describe() string {
	if s.san != "" {
		return s.san
	}
	return s.sanRegexp
}

func (s signer) policy(sum []byte) (verify.PolicyBuilder, error) {
	san, err := verify.NewSANMatcher(s.san, s.sanRegexp)
	if err != nil {
		return verify.PolicyBuilder{}, err
	}
	issuer, err := verify.NewIssuerMatcher(s.issuer, "")
	if err != nil {
		return verify.PolicyBuilder{}, err
	}
	id, err := verify.NewCertificateIdentity(san, issuer, certificate.Extensions{
		GithubWorkflowRepository: s.repository,
		GithubWorkflowTrigger:    s.trigger,
	})
	if err != nil {
		return verify.PolicyBuilder{}, err
	}
	return verify.NewPolicy(verify.WithArtifactDigest("sha256", sum), verify.WithCertificateIdentity(id)), nil
}

// bundlesOf reads the Sigstore bundles attached to ref as OCI referrers.
func (v *CosignVerifier) bundlesOf(ctx context.Context, ref name.Digest) ([]*bundle.Bundle, error) {
	opts := v.remoteOptions(ctx)
	idx, err := remote.Referrers(ref, append(opts, remote.WithFilter("artifactType", bundleArtifactType))...)
	if err != nil {
		return nil, fmt.Errorf("listing the signatures of %s: %w", ref, err)
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	var out []*bundle.Bundle
	for _, d := range m.Manifests {
		// A registry that cannot filter returns every referrer: keep the ones
		// that are bundles.
		if d.ArtifactType != bundleArtifactType {
			continue
		}
		if len(out) == maxBundles {
			break
		}
		img, err := remote.Image(ref.Context().Digest(d.Digest.String()), opts...)
		if err != nil {
			return nil, err
		}
		raw, err := readSingleLayer(img, bundleMediaTypePrefix, true)
		if err != nil {
			return nil, err
		}
		b := &bundle.Bundle{}
		if err := b.UnmarshalJSON(raw); err != nil {
			// One malformed bundle must not hide a good one next to it.
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Sigstore signature is attached to %s", ref)
	}
	return out, nil
}
