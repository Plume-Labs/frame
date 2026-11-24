#!/usr/bin/env bash
# check-multiarch.sh — read-only: for every image reference found in the given
# files, report whether the registry publishes a linux/arm64 manifest.
#
# Usage: hack/check-multiarch.sh FILE...        (Dockerfiles, compose, values, k8s YAML)
# Needs: docker buildx (or podman), network access to the registries. Writes nothing,
# touches no cluster. Exit 1 if at least one image has no arm64 variant.
set -euo pipefail

[ "$#" -ge 1 ] || { echo "usage: $0 FILE..." >&2; exit 2; }

inspect() {
  if command -v docker >/dev/null 2>&1; then
    docker buildx imagetools inspect "$1" 2>&1
  elif command -v podman >/dev/null 2>&1; then
    podman manifest inspect "docker://$1" 2>&1
  else
    echo "need docker buildx or podman" >&2; exit 2
  fi
}

# Image refs: `FROM x`, `image: x`, `repository: x` + `tag:` are NOT resolved
# (templated refs are skipped on purpose and listed as SKIP).
mapfile -t refs < <(
  grep -hEi '^\s*(FROM|image:)\s+' "$@" \
    | sed -E 's/^\s*(FROM|image:)\s+//I; s/--platform=\S+\s+//; s/\s+AS\s+.*//I; s/^["'\'']//; s/["'\'']\s*$//; s/\s+#.*//' \
    | grep -vE '^$|^scratch$' | sort -u
)

fail=0
for r in "${refs[@]}"; do
  case "$r" in
    *'{{'*|*'$'*|*REPLACE_*|frame-*:latest|*cluster-control:latest) echo "SKIP  $r (templated/local)"; continue ;;
  esac
  if out=$(inspect "$r") ; then
    if grep -qE 'linux/arm64|"architecture": "arm64"' <<<"$out"; then echo "OK    $r"
    else echo "NOARM $r"; fail=1; fi
  else
    echo "ERR   $r (not inspectable)"; fail=1
  fi
done
exit "$fail"
