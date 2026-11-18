#!/usr/bin/env bash
# Fails when a headline doc states a wrong count of CRDs, Go binaries or
# Dockerfiles. The numbers are derived from the tree (config/crd/bases, cmd/,
# Dockerfile*), so adding a CRD or a binary makes the docs fail until they are
# updated, instead of rotting for weeks (the audit of 2026-11-18 found "nine
# CRDs" and "three binaries" against 19 and 6).
#
# Only the files in DOCS are checked: history (roadmap, specs, release notes,
# the rehearsal logs in upgrading.md) legitimately mentions old counts.
set -euo pipefail
cd "$(dirname "$0")/.."

crds=$(find config/crd/bases -maxdepth 1 -name '*.yaml' | wc -l)
# One binary per cmd/<name>/ directory, plus the manager in cmd/main.go.
bins=$(( $(find cmd -mindepth 1 -maxdepth 1 -type d | wc -l) + $(find cmd -maxdepth 1 -name main.go | wc -l) ))
dockerfiles=$(find . -maxdepth 1 -name 'Dockerfile*' | wc -l)

DOCS=(README.md AGENTS.md docs/README.md docs/architecture.md docs/deployment.md docs/api.md docs/crd-reference.md)

num() { # word or digits -> number, empty if not a count word
  case "${1,,}" in
    one) echo 1;; two) echo 2;; three) echo 3;; four) echo 4;; five) echo 5;;
    six) echo 6;; seven) echo 7;; eight) echo 8;; nine) echo 9;; ten) echo 10;;
    eleven) echo 11;; twelve) echo 12;; [0-9]*) echo "$1";;
  esac
}

fail=0
check() { # label, actual, regex noun
  local label=$1 actual=$2 noun=$3 f line word n
  for f in "${DOCS[@]}"; do
    # A renamed or deleted doc must not turn its check into a silent no-op.
    [ -f "$f" ] || { echo "$f: listed in DOCS but missing" >&2; fail=1; continue; }
    while IFS= read -r hit; do
      line=${hit%%:*}
      word=$(grep -oiE "\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|[0-9]+) ${noun}" <<<"$hit" | head -n1 | awk '{print $1}')
      n=$(num "$word")
      if [ -n "$n" ] && [ "$n" != "$actual" ]; then
        echo "$f:$line: says \"$word $label\", the tree has $actual" >&2
        fail=1
      fi
    done < <(grep -niE "\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|[0-9]+) ${noun}" "$f" || true)
  done
}

check CRDs "$crds" 'CRDs'
check "Go binaries" "$bins" 'Go binaries'
check Dockerfiles "$dockerfiles" 'Dockerfiles'

[ "$fail" -eq 0 ] && echo "doc scope: $crds CRDs, $bins binaries, $dockerfiles Dockerfiles, docs agree"
exit "$fail"
