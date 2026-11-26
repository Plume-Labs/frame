#!/usr/bin/env bash
# Verifies a published release: the manifest signature, then the signature of
# every image it names (by digest). Read-only. A failure is never bypassed.
#
# Usage: hack/verify-release.sh <version>      (e.g. 1.0.0)
# Needs: cosign, jq, oras (or crane) on PATH.
set -euo pipefail

version="${1:?version}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "bad version: $version" >&2; exit 2; }
owner="${OWNER:-plume-labs}"
repo="${REPO:-Plume-Labs/frame}"
# Identity pinned to the build workflow on a release tag (docs/deployment.md).
identity="https://github.com/${repo}/.github/workflows/build.yml@refs/tags/v${version}"
issuer="https://token.actions.githubusercontent.com"

ref="ghcr.io/${owner}/hearthrack-release:${version}"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

cosign verify --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$ref" >/dev/null
oras pull "$ref" -o "$work" >/dev/null
jq -e --arg v "$version" '.schema == 1 and .version == $v' "$work/release.json" >/dev/null

jq -r '.images | to_entries[] | .value' "$work/release.json" | while read -r img; do
  [[ "$img" == *@sha256:* ]] || { echo "image not pinned by digest: $img" >&2; exit 1; }
  cosign verify --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$img" >/dev/null
  echo "ok  $img"
done
echo "release ${version}: manifest and all images verified"
