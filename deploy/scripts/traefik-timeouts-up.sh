#!/usr/bin/env bash
# traefik-timeouts-up.sh — raise Traefik's per-entrypoint idle timeout so
# long-idle SSE streams (agent runs waiting on a tool call, sandbox exec,
# etc.) don't get silently dropped.
#
# Why a HelmChartConfig: k3s bundles Traefik via its own `helm.cattle.io`
# HelmChart resource (see deploy/scripts/test-cluster-up.sh's header — this
# is separate from that script, since it's a one-time cluster tunable, not
# part of the app-provisioning flow). A HelmChartConfig of the same
# name+namespace is how k3s lets you layer extra Helm values on top of its
# defaults without hand-editing the HelmChart object it reconciles.
#
# readTimeout/writeTimeout already default to unlimited (0) in Traefik — only
# idleTimeout (default 180s: max gap between reads/writes on an open
# connection) is worth raising here. This replaces the
# nginx.ingress.kubernetes.io/proxy-read-timeout / proxy-send-timeout
# annotations on Neura's ingress, which the cluster's actual controller
# (Traefik, not nginx) silently ignored. proxy-buffering has no Traefik
# equivalent needed — Traefik streams responses by default, unlike nginx.
#
# Was broken on this cluster from 2026-07-28 to 2026-07-29: applying ANY
# HelmChartConfig for `traefik` made k3s's helm-install-traefik job fail
# with "Required CRDs are missing", despite every traefik.io CRD being
# present. Root cause (found 2026-07-28, fixed same day, verified by
# reapplying this script 2026-07-29): unrelated Gateway API tooling had
# pinned v1.3.0 and applied it after k3s's own CRDs, downgrading
# ReferenceGrant to v1beta1-only — the chart needs v1 (Gateway API >= 1.4).
# Gateway API is now pinned to v1.5.1; this script applies cleanly and the
# live traefik pod picks up idleTimeout=1h in its args.
set -euo pipefail

export KUBECONFIG="${KUBECONFIG:-$HOME/Neura/.test-cluster/kubeconfig-neura-test.yaml}"

say() { echo -e "\n\033[1;36m==>\033[0m $*"; }

say "Traefik entrypoint idleTimeout -> 1h"
kubectl apply -f - <<'EOF'
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: traefik
  namespace: kube-system
spec:
  valuesContent: |-
    ports:
      web:
        transport:
          respondingTimeouts:
            idleTimeout: 1h
      websecure:
        transport:
          respondingTimeouts:
            idleTimeout: 1h
EOF

say "k3s reconciles the underlying HelmChart on its own; watch it with:"
echo "  kubectl -n kube-system rollout status deploy/traefik"
