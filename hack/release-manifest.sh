#!/usr/bin/env bash
# Builds release.json for a tagged release: the signed source of truth the
# in-console updater reads (docs/specs/2026-11-24-mise-a-jour-en-un-clic.md §3.1).
#
# Usage: hack/release-manifest.sh <version> <channel> <owner> > release.json
#   version  semver without the leading v (1.0.0, 0.9.0-beta.1)
#   channel  stable | beta
#   owner    GHCR owner, lowercase (plume-labs)
#
# Images are resolved BY DIGEST from the registry (the tag was just pushed by
# the build job); a tag can move, a digest cannot. Read-only: it pushes and
# signs nothing. Override the lookup with DIGEST_<NAME>=sha256:... (tests).
set -euo pipefail

version="${1:?version}"; channel="${2:?channel}"; owner="${3:?owner}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "bad version: $version" >&2; exit 2; }
[[ "$channel" == stable || "$channel" == beta ]] || { echo "bad channel: $channel" >&2; exit 2; }
[[ "$owner" =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "bad owner: $owner" >&2; exit 2; }

images=(controller ui uiproxy authd agent provisiond updater)
json_images='{}'
for name in "${images[@]}"; do
  var="DIGEST_${name^^}"
  digest="${!var:-}"
  if [[ -z "$digest" ]]; then
    digest="$(docker buildx imagetools inspect "ghcr.io/${owner}/hearthrack-${name}:${version}" --format '{{.Manifest.Digest}}')"
  fi
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no valid digest for ${name}: '${digest}'" >&2; exit 1; }
  json_images="$(jq -c --arg n "$name" --arg r "ghcr.io/${owner}/hearthrack-${name}@${digest}" '. + {($n): $r}' <<<"$json_images")"
done

# minFrom and rollbackSafeTo are release decisions, read from a checked-in file
# so a human reviews them in a PR rather than the pipeline inventing them.
policy="${RELEASE_POLICY_FILE:-release-policy.json}"
min_from='null'; rollback_safe_to='null'
if [[ -f "$policy" ]]; then
  min_from="$(jq -c '.minFrom // null' "$policy")"
  rollback_safe_to="$(jq -c '.rollbackSafeTo // null' "$policy")"
fi

jq -n --arg version "$version" --arg channel "$channel" \
  --arg publishedAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson images "$json_images" \
  --argjson minFrom "$min_from" --argjson rollbackSafeTo "$rollback_safe_to" \
  '{schema: 1, version: $version, channel: $channel, publishedAt: $publishedAt,
    images: $images, minFrom: $minFrom, rollbackSafeTo: $rollbackSafeTo}'
