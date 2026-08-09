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
#                CI_PLACEHOLDER_IMAGE=some/image (image.repository has no
#                real default and is `required`; any syntactically valid
#                value works here, nothing is ever pulled)
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
CI_PLACEHOLDER_IMAGE="${CI_PLACEHOLDER_IMAGE:-ci-placeholder.invalid/frame}"

command -v "$KUSTOMIZE" >/dev/null 2>&1 || { echo "kustomize not found at $KUSTOMIZE (run 'make kustomize')" >&2; exit 1; }
command -v "$HELM" >/dev/null 2>&1 || { echo "helm not found ($HELM)" >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "go not found — required to run the YAML extractor below" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "jq not found — required to process the extractor's output" >&2; exit 1; }

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

# --- YAML -> JSONL extractor -------------------------------------------------
# Deliberately Go, not PyYAML: this repo already requires the Go toolchain to
# build anything, and k8s.io/apimachinery + sigs.k8s.io/yaml are already
# go.sum dependencies (kubebuilder pulls them in transitively), so this adds
# no new dependency and no network call — unlike the previous
# `pip install --user pyyaml`, which was a network install running under
# `set -e` in a CI gate (M-4: fails outright rather than degrading on a
# PEP 668 "externally-managed-environment" runner, and is one more thing that
# can flake). `go run` compiles this file fresh each run; it is not part of
# the module (nothing under hack/ is a tracked .go file) so it is never
# built, vetted or linted as part of the project.
cat > "$tmpdir/yaml2jsonl.go" <<'GO'
package main

import (
	"bufio"
	"fmt"
	"os"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Reads a multi-document YAML stream and prints one compact JSON object per
// line for every document that has a non-empty "kind" (skipping blank
// documents, e.g. a leading "---"). Exits non-zero on the first document
// that fails to parse, so a corrupt manifest fails the parity check loudly
// instead of silently vanishing from the comparison.
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: yaml2jsonl <file>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	reader := k8syaml.NewYAMLReader(bufio.NewReader(f))
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for {
		chunk, err := reader.Read()
		if err != nil {
			break
		}
		jsonBytes, err := yaml.YAMLToJSON(chunk)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: failed to parse a YAML document: %v\n", os.Args[1], err)
			os.Exit(1)
		}
		var doc map[string]interface{}
		if err := yaml.Unmarshal(jsonBytes, &doc); err != nil {
			fmt.Fprintf(os.Stderr, "%s: failed to unmarshal a YAML document: %v\n", os.Args[1], err)
			os.Exit(1)
		}
		if kind, _ := doc["kind"].(string); kind == "" {
			continue
		}
		fmt.Fprintln(out, string(jsonBytes))
	}
}
GO

extract_jsonl() {
  # $1 = input YAML file; prints JSONL to stdout.
  (cd "$ROOT_DIR" && go run "$tmpdir/yaml2jsonl.go" "$1")
}

triples() {
  # $1 = JSONL file; prints sorted, de-duplicated "kind|namespace|name" lines.
  jq -r '"\(.kind)|\(.metadata.namespace // "")|\(.metadata.name // "")"' "$1" | sort -u
}

# --- kustomize side ---------------------------------------------------------
"$KUSTOMIZE" build "$ROOT_DIR/config/default" > "$tmpdir/kustomize.yaml"
extract_jsonl "$tmpdir/kustomize.yaml" > "$tmpdir/kustomize.jsonl"
triples "$tmpdir/kustomize.jsonl" > "$tmpdir/kustomize.set"

# Namespace: kustomize renders one because config/default owns/prefixes the
# namespace. The chart deliberately does NOT template a Namespace object —
# it's supplied externally by `helm install -n frame-system --create-namespace`
# (see charts/frame/README.md, "Name compatibility"). Strip it before
# diffing so it isn't reported as a kustomize-only resource.
grep -v '^Namespace|' "$tmpdir/kustomize.set" > "$tmpdir/kustomize.set.filtered"

# --- helm side: default values ----------------------------------------------
"$HELM" template frame "$CHART_DIR" --namespace frame-system \
  --set image.repository="$CI_PLACEHOLDER_IMAGE" \
  > "$tmpdir/helm-default.yaml"
extract_jsonl "$tmpdir/helm-default.yaml" > "$tmpdir/helm-default.jsonl"
triples "$tmpdir/helm-default.jsonl" > "$tmpdir/helm-default.set"

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
# kustomization.yaml, so kustomize never renders them as part of
# `kustomize build config/default`. The chart renders them behind values
# (metrics.serviceMonitor.enabled, networkPolicy.enabled) instead of requiring
# a hand-edited kustomization.
#
# This allow-list is by *exact* kind|namespace|name, not by kind (I-4): an
# earlier version of this script allow-listed the two kinds wholesale, which
# meant the chart could rename, mis-namespace, or stop rendering these three
# resources entirely and the script would still print OK (nothing helm-only,
# nothing kustomize-only). Allow-listing exact identities plus asserting their
# presence closes both holes — anything else appearing only on one side still
# fails the script.
EXPECTED_EXTRAS=(
  "NetworkPolicy|frame-system|frame-allow-metrics-traffic"
  "NetworkPolicy|frame-system|frame-allow-webhook-traffic"
  "ServiceMonitor|frame-system|frame-controller-manager-metrics-monitor"
)

"$HELM" template frame "$CHART_DIR" --namespace frame-system \
  --set image.repository="$CI_PLACEHOLDER_IMAGE" \
  --set networkPolicy.enabled=true \
  --set metrics.serviceMonitor.enabled=true \
  > "$tmpdir/helm-extras.yaml"
extract_jsonl "$tmpdir/helm-extras.yaml" > "$tmpdir/helm-extras.jsonl"
triples "$tmpdir/helm-extras.jsonl" > "$tmpdir/helm-extras.set"

comm -13 "$tmpdir/kustomize.set.filtered" "$tmpdir/helm-extras.set" > "$tmpdir/helm-only" || true
comm -23 "$tmpdir/kustomize.set.filtered" "$tmpdir/helm-extras.set" > "$tmpdir/kustomize-only" || true

echo "== extras parity: helm template (networkPolicy + serviceMonitor on) vs kustomize =="
fail=0

if [ -s "$tmpdir/kustomize-only" ]; then
  echo "FAIL: kustomize renders resources the chart is missing even with extras enabled:" >&2
  cat "$tmpdir/kustomize-only" >&2
  fail=1
fi

# Every allow-listed extra must actually be present (catches "stopped
# rendering it entirely", which a helm-only/kustomize-only set diff alone
# cannot see since both sides would just be silent about it).
for expected in "${EXPECTED_EXTRAS[@]}"; do
  if ! grep -qxF "$expected" "$tmpdir/helm-extras.set"; then
    echo "FAIL: expected extra resource is missing from the chart's render: $expected" >&2
    fail=1
  fi
done

# Anything helm-only that ISN'T exactly one of the allow-listed triples fails
# (catches renames, wrong namespace, or a genuinely new undocumented resource).
while IFS= read -r line; do
  [ -z "$line" ] && continue
  allowed=0
  for expected in "${EXPECTED_EXTRAS[@]}"; do
    if [ "$line" = "$expected" ]; then
      allowed=1
      break
    fi
  done
  if [ "$allowed" -ne 1 ]; then
    echo "FAIL: chart renders a resource kustomize has no equivalent for, and it is not exactly one of the allow-listed extras: $line" >&2
    fail=1
  fi
done < "$tmpdir/helm-only"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "OK: exactly the allow-listed extras (${EXPECTED_EXTRAS[*]}) differ, and only in the expected direction."
echo

# --- content diff: the extras against their own standalone kustomize builds -
# Presence and naming aren't enough (I-4 also flagged that the *contents* of
# these three were never compared to anything) — config/network-policy and
# config/prometheus both build standalone, so diff the chart's rendering
# against them directly. The one documented exception: the webhook
# NetworkPolicy's ports (I-3 — kustomize's copy uses the Service port, 443,
# where NetworkPolicy ingress ports are actually pod ports; the chart fixes
# this to 9443, so that one field is deliberately expected to differ).
echo "== extras content diff: chart's ServiceMonitor/NetworkPolicies vs config/prometheus, config/network-policy =="

"$KUSTOMIZE" build "$ROOT_DIR/config/network-policy" > "$tmpdir/np-standalone.yaml"
"$KUSTOMIZE" build "$ROOT_DIR/config/prometheus" > "$tmpdir/prom-standalone.yaml"
extract_jsonl "$tmpdir/np-standalone.yaml" > "$tmpdir/np-standalone.jsonl"
extract_jsonl "$tmpdir/prom-standalone.yaml" > "$tmpdir/prom-standalone.jsonl"

content_fail=0

# allow-metrics-traffic: no documented divergence, so the whole .spec must match.
a_metrics="$(jq -S 'select(.kind=="NetworkPolicy" and (.metadata.name | endswith("allow-metrics-traffic"))) | .spec' "$tmpdir/helm-extras.jsonl")"
b_metrics="$(jq -S 'select(.kind=="NetworkPolicy" and .metadata.name=="allow-metrics-traffic") | .spec' "$tmpdir/np-standalone.jsonl")"
if [ "$a_metrics" != "$b_metrics" ]; then
  echo "FAIL: allow-metrics-traffic NetworkPolicy content differs from config/network-policy:" >&2
  diff <(echo "$a_metrics") <(echo "$b_metrics") >&2 || true
  content_fail=1
fi

# allow-webhook-traffic: compare everything except .spec.ingress[0].ports,
# which is the intended, documented I-3 fix (9443 pod port vs kustomize's
# buggy 443 service port) — then separately assert the fix is actually in
# place, so this exception can't silently mask a real regression either.
a_webhook="$(jq -S 'select(.kind=="NetworkPolicy" and (.metadata.name | endswith("allow-webhook-traffic"))) | .spec | .ingress[0].ports = "IGNORED (see I-3)"' "$tmpdir/helm-extras.jsonl")"
b_webhook="$(jq -S 'select(.kind=="NetworkPolicy" and .metadata.name=="allow-webhook-traffic") | .spec | .ingress[0].ports = "IGNORED (see I-3)"' "$tmpdir/np-standalone.jsonl")"
if [ "$a_webhook" != "$b_webhook" ]; then
  echo "FAIL: allow-webhook-traffic NetworkPolicy content differs from config/network-policy (beyond the documented I-3 ports fix):" >&2
  diff <(echo "$a_webhook") <(echo "$b_webhook") >&2 || true
  content_fail=1
fi
webhook_port="$(jq -r 'select(.kind=="NetworkPolicy" and (.metadata.name | endswith("allow-webhook-traffic"))) | .spec.ingress[0].ports[0].port' "$tmpdir/helm-extras.jsonl")"
if [ "$webhook_port" != "9443" ]; then
  echo "FAIL: chart's allow-webhook-traffic NetworkPolicy ingress port is $webhook_port, expected 9443 (the webhook server's actual pod port — see templates/networkpolicy.yaml)." >&2
  content_fail=1
fi

# ServiceMonitor: no documented divergence, so the whole .spec must match.
a_sm="$(jq -S 'select(.kind=="ServiceMonitor") | .spec' "$tmpdir/helm-extras.jsonl")"
b_sm="$(jq -S 'select(.kind=="ServiceMonitor") | .spec' "$tmpdir/prom-standalone.jsonl")"
if [ "$a_sm" != "$b_sm" ]; then
  echo "FAIL: ServiceMonitor content differs from config/prometheus/monitor.yaml:" >&2
  diff <(echo "$a_sm") <(echo "$b_sm") >&2 || true
  content_fail=1
fi

if [ "$content_fail" -ne 0 ]; then
  exit 1
fi
echo "OK: extras' content matches their config/ source, modulo the documented I-3 port fix."
