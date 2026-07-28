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

# modern_ebpf is the only workable driver on this cluster's kernel (7.0.0-28):
#   - kmod: no prebuilt module is published for it (download.falco.org 404s) and
#     the nodes carry no kernel headers to build one;
#   - ebpf (legacy probe): removed from chart 9.x, see its BREAKING-CHANGES.md.
# KNOWN ISSUE, not a misconfiguration: on this kernel the modern probe
# periodically emits an event the userspace parser rejects — "could not parse
# param 2 (name) for event ... of type 307 (openat) ... expected length 2, found
# 227", always on an oversized cgroup path. Falco exits 1 and is restarted by the
# kubelet every few minutes, losing events in between. Falco 0.44.1 / chart 9.1.0
# is the latest release as of 2026-07-28 and still has it. Re-check on the next
# Falco release before reaching for workarounds.
helm upgrade -i falco falcosecurity/falco -n falco --create-namespace \
  --set driver.kind=modern_ebpf \
  --set falcosidekick.enabled=true \
  --set falcosidekick.replicaCount=1 \
  --set falcosidekick.webui.enabled=false \
  --set collectors.kubernetes.enabled=true \
  --set falco.json_output=true \
  --set resources.requests.cpu=100m \
  --set resources.requests.memory=256Mi \
  --set falcosidekick.config.alertmanager.hostport=http://kps-kube-prometheus-stack-alertmanager.monitoring:9093 \
  --set falcosidekick.config.alertmanager.endpoint=/api/v2/alerts \
  --set falcosidekick.config.alertmanager.minimumpriority=critical

kubectl -n falco rollout status ds/falco --timeout=300s
kubectl -n falco rollout status deploy/falco-falcosidekick --timeout=180s
# The chart has no commonLabels knob (only podLabels, which the Applications
# view doesn't read) — label the objects directly so falco + falco-k8saudit
# (two separate Helm releases, each with their own instance label) group
# into one Frame Applications card instead of splitting into three.
kubectl -n falco label ds/falco deploy/falco-falcosidekick deploy/falco-k8s-metacollector \
  app.kubernetes.io/part-of=falco --overwrite 2>/dev/null || true

say "trivy-operator (security posture: image CVEs + workload misconfigs)"
helm repo add aqua https://aquasecurity.github.io/helm-charts/ >/dev/null 2>&1 || true
helm repo update aqua >/dev/null 2>&1 || true
helm upgrade -i trivy-operator aqua/trivy-operator -n trivy-system --create-namespace \
  --set trivy.ignoreUnfixed=true \
  --set trivyOperator.scanJobsConcurrentLimit=2 \
  --set operator.builtInTrivyServer=true \
  --set operator.builtInServerRegistryInsecure=true \
  --set trivy.insecureRegistries.local=192.168.2.201:30500 \
  --set trivy.resources.limits.memory=1Gi
kubectl -n trivy-system rollout status deploy/trivy-operator --timeout=180s
kubectl -n trivy-system rollout status statefulset/trivy-server --timeout=180s
# builtInTrivyServer switches trivy.mode=ClientServer: scan job containers
# become thin clients talking to one shared trivy-server instead of each
# opening its own local BoltDB cache — fixes the lock-contention crash that
# hit any multi-container target pod (they all shared one job-pod emptyDir
# /tmp, so sibling scan containers raced for the same cache lock).
# The Frame Security screen reads these CRDs (VulnerabilityReport / ConfigAuditReport)
# via the pod SA — cluster-control-viewer includes aquasecurity.github.io (rbac.yaml).

say "Falco k8saudit plugin (API-level attack detection → same falcosidekick)"
helm upgrade -i falco-k8saudit falcosecurity/falco -n falco \
  -f deploy/samples/test-cluster/falco-k8saudit-values.yaml
kubectl -n falco rollout status deploy/falco-k8saudit --timeout=240s
kubectl -n falco label deploy/falco-k8saudit app.kubernetes.io/part-of=falco --overwrite

cat <<'NOTE'

  [manual step — control-plane node, needs root]
  The k8saudit receiver is up (NodePort 32765). To feed it, point the k3s
  apiserver audit log at it (privileged: restarts k3s, auto-reverts on failure):

      sudo deploy/scripts/enable-k8s-audit.sh     # run ON the cp node

  After that, API-level attacks (privileged pod, exec, RBAC change, secret
  access) appear on the Frame Security screen with source=k8s_audit.

NOTE

say "Tetragon (eBPF process + network observability — CNI-agnostic, no swap)"
helm repo add cilium https://helm.cilium.io >/dev/null 2>&1 || true
helm repo update cilium >/dev/null 2>&1 || true
helm upgrade -i tetragon cilium/tetragon -n tetragon --create-namespace \
  --set tetragon.enablePolicyFilter=true
kubectl -n tetragon rollout status ds/tetragon --timeout=300s
# TracingPolicy: observe outbound TCP connects → PROCESS_KPROBE network events
kubectl apply -f deploy/samples/test-cluster/tetragon-netpolicy.yaml

say "Done. Falco (syscall + k8saudit) → Falcosidekick :2801; trivy-operator → aquasecurity.github.io CRDs; Tetragon :2112. All feed the Frame Security screen."
