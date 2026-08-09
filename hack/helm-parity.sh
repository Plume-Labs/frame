#!/usr/bin/env bash
# Compares the resource set `helm template` renders for charts/frame against
# `kustomize build config/default` — the two install paths Frame supports
# (kustomize for day-to-day dev, Helm for the versioned/published path) — so
# they can never silently diverge. Fails when either side grows a resource
# the other lacks, except for the intended, documented differences allow-listed
# below.
#
# Usage: ./hack/helm-parity.sh
# Env overrides: KUSTOMIZE=path/to/kustomize HELM=path/to/helm
set -euo pipefail

# `comm` requires both inputs sorted per the active locale's collation order;
# force the C locale everywhere in this script so the extractor's sort order
# (plain byte order) always matches what `comm` expects, regardless of the
# invoking shell's locale.
export LC_ALL=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART_DIR="$ROOT_DIR/charts/frame"
KUSTOMIZE="${KUSTOMIZE:-$ROOT_DIR/bin/kustomize}"
HELM="${HELM:-helm}"

command -v "$KUSTOMIZE" >/dev/null 2>&1 || { echo "kustomize not found at $KUSTOMIZE (run 'make kustomize')" >&2; exit 1; }
command -v "$HELM" >/dev/null 2>&1 || { echo "helm not found ($HELM)" >&2; exit 1; }

# The extractor needs PyYAML; install it quietly if this environment doesn't
# already have it (CI runners generally do, but don't assume).
if ! python3 -c "import yaml" >/dev/null 2>&1; then
  python3 -m pip install --quiet --user pyyaml
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

extract() {
  # Emits sorted, de-duplicated "kind|namespace|name" lines for every
  # document in a multi-doc YAML stream that has a `kind`.
  python3 - "$1" <<'PY'
import sys
import yaml

with open(sys.argv[1]) as f:
    docs = yaml.safe_load_all(f)
    rows = set()
    for d in docs:
        if not d or "kind" not in d:
            continue
        md = d.get("metadata") or {}
        rows.add(f"{d['kind']}|{md.get('namespace', '')}|{md.get('name', '')}")
for row in sorted(rows):
    print(row)
PY
}

# --- kustomize side ---------------------------------------------------------
"$KUSTOMIZE" build "$ROOT_DIR/config/default" > "$tmpdir/kustomize.yaml"
extract "$tmpdir/kustomize.yaml" > "$tmpdir/kustomize.set"

# Namespace: kustomize renders one because config/default owns/prefixes the
# namespace. The chart deliberately does NOT template a Namespace object —
# it's supplied externally by `helm install -n frame-system --create-namespace`
# (see charts/frame/README.md, "Name compatibility"). Strip it before
# diffing so it isn't reported as a kustomize-only resource.
grep -v '^Namespace|' "$tmpdir/kustomize.set" > "$tmpdir/kustomize.set.filtered"

# --- helm side: default values ----------------------------------------------
"$HELM" template frame "$CHART_DIR" --namespace frame-system > "$tmpdir/helm-default.yaml"
extract "$tmpdir/helm-default.yaml" > "$tmpdir/helm-default.set"

echo "== default parity: helm template vs kustomize build config/default (minus Namespace) =="
if ! diff -u "$tmpdir/kustomize.set.filtered" "$tmpdir/helm-default.set"; then
  echo "FAIL: default 'helm template' output does not match 'kustomize build config/default'." >&2
  echo "If this is an intended, permanent difference, allow-list it below with a reason — do not silently filter it." >&2
  exit 1
fi
echo "OK: identical resource sets."
echo

# --- helm side: opt-in extras ------------------------------------------------
# ServiceMonitor and the two NetworkPolicies live in config/ (config/prometheus,
# config/network-policy) but are commented out of config/default's
# kustomization.yaml, so kustomize never renders them today. The chart renders
# them behind values (metrics.serviceMonitor.enabled, networkPolicy.enabled)
# instead of requiring a hand-edited kustomization — that is a one-directional,
# intended difference: allow-list these two kinds as helm-only when the values
# that turn them on are set. Anything else appearing only on one side still
# fails the script.
ALLOW_HELM_ONLY_KINDS=(ServiceMonitor NetworkPolicy)

"$HELM" template frame "$CHART_DIR" --namespace frame-system \
  --set networkPolicy.enabled=true \
  --set metrics.serviceMonitor.enabled=true \
  > "$tmpdir/helm-extras.yaml"
extract "$tmpdir/helm-extras.yaml" > "$tmpdir/helm-extras.set"

comm -13 "$tmpdir/kustomize.set.filtered" "$tmpdir/helm-extras.set" > "$tmpdir/helm-only" || true
comm -23 "$tmpdir/kustomize.set.filtered" "$tmpdir/helm-extras.set" > "$tmpdir/kustomize-only" || true

echo "== extras parity: helm template (networkPolicy + serviceMonitor on) vs kustomize =="
fail=0

if [ -s "$tmpdir/kustomize-only" ]; then
  echo "FAIL: kustomize renders resources the chart is missing even with extras enabled:" >&2
  cat "$tmpdir/kustomize-only" >&2
  fail=1
fi

while IFS= read -r line; do
  [ -z "$line" ] && continue
  kind="${line%%|*}"
  allowed=0
  for k in "${ALLOW_HELM_ONLY_KINDS[@]}"; do
    if [ "$kind" = "$k" ]; then
      allowed=1
      break
    fi
  done
  if [ "$allowed" -ne 1 ]; then
    echo "FAIL: chart renders a resource kustomize has no equivalent for, and its kind is not on the allow-list: $line" >&2
    fail=1
  fi
done < "$tmpdir/helm-only"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "OK: only allow-listed kinds (${ALLOW_HELM_ONLY_KINDS[*]}) differ, and only in the expected direction."
