#!/usr/bin/env bash
# Renders a kustomize directory under config/ with the controller image set,
# from a throwaway copy of config/ (mktemp). `kustomize edit set image` run
# in place — what `make deploy` used to do — rewrites
# config/manager/kustomization.yaml with the image, i.e. a private registry
# address, one `git add` away from a public repository.
#
# Usage: hack/render-overlay.sh <dir under config/> <image> [provisiond-image]
#   e.g. hack/render-overlay.sh overlays/production \
#          registry.example/frame-controller:v1.2.0 registry.example/frame-provisiond:v1.2.0
# Without provisiond-image, frame-provisiond keeps config/provisiond's
# frame-provisiond:latest (a locally built image: dev/e2e clusters only).
# Env: KUSTOMIZE (default bin/kustomize).
set -euo pipefail

dir="${1:-}"
img="${2:-}"
img_provisiond="${3:-}"
if [ -z "$dir" ] || [ -z "$img" ]; then
  echo "usage: $0 <dir under config/> <image> [provisiond-image]" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
kustomize="${KUSTOMIZE:-$root/bin/kustomize}"
[ -d "$root/config/$dir" ] || { echo "$0: no config/$dir" >&2; exit 2; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cp -r "$root/config" "$tmp/config"
(cd "$tmp/config/manager" && "$kustomize" edit set image "controller=$img")
if [ -n "$img_provisiond" ]; then
  (cd "$tmp/config/provisiond" && "$kustomize" edit set image "frame-provisiond=$img_provisiond")
fi
"$kustomize" build "$tmp/config/$dir"
