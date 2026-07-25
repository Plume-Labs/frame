#!/usr/bin/env bash
# security-up.sh — runtime threat detection for the cluster: Falco (eBPF syscall
# monitoring) + Falcosidekick (exposes detections as Prometheus metrics that the
# Frame Security screen reads via the pod-proxy).
#
# Falco is CNI-agnostic (kernel/syscall level) — detects shell-in-container,
# privilege escalation, sensitive-file reads, crypto-miners, unexpected exec,
# etc. modern_ebpf needs kernel >= 5.8 (CO-RE, no kernel headers required).
set -euo pipefail

say() { echo -e "\n\033[1;31m==>\033[0m $*"; }

say "Falco + Falcosidekick (runtime security)"
helm repo add falcosecurity https://falcosecurity.github.io/charts >/dev/null 2>&1 || true
helm repo update falcosecurity >/dev/null 2>&1 || true

helm upgrade -i falco falcosecurity/falco -n falco --create-namespace \
  --set driver.kind=modern_ebpf \
  --set falcosidekick.enabled=true \
  --set falcosidekick.replicaCount=1 \
  --set falcosidekick.webui.enabled=false \
  --set collectors.kubernetes.enabled=true \
  --set falco.json_output=true \
  --set resources.requests.cpu=100m \
  --set resources.requests.memory=256Mi

kubectl -n falco rollout status ds/falco --timeout=300s
kubectl -n falco rollout status deploy/falco-falcosidekick --timeout=180s
say "Done. Falcosidekick metrics: falco-falcosidekick.falco.svc:2801/metrics (Frame Security screen reads this)."
