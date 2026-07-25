#!/usr/bin/env bash
# enable-cilium.sh — replace flannel with Cilium as the k3s CNI, enabling Hubble
# network flow visibility (Tetragon already runs standalone via security-up.sh).
#
# ⚠️  FRESH / REBUILD CLUSTER ONLY. This is NOT a hot-swap: it disables flannel
# and restarts k3s on every node, so pod networking drops until Cilium is up and
# every pod is restarted. On a running cluster with stateful workloads (Ceph,
# postgres) do NOT run this against live data — rebuild instead.
#
# Privileged: edits /etc/rancher/k3s/config.yaml and restarts k3s on each node.
# Run on the control-plane node first, then each agent, THEN install Cilium.
set -euo pipefail
[ "$(id -u)" = "0" ] || { echo "run as root (sudo)"; exit 1; }

ROLE="${1:-}"   # "server" (control-plane) or "agent"
case "$ROLE" in
  server|agent) ;;
  *) echo "usage: sudo $0 <server|agent>   (run on cp with 'server', on each worker with 'agent')"; exit 1 ;;
esac

CFG=/etc/rancher/k3s/config.yaml
touch "$CFG"; cp "$CFG" "$CFG.bak.pre-cilium" 2>/dev/null || true
# Disable flannel + k3s network policy so Cilium owns the dataplane.
# (kube-proxy left in place; Cilium can replace it later with kubeProxyReplacement.)
if ! grep -q 'flannel-backend' "$CFG"; then
  cat >> "$CFG" <<'CFGEOF'
flannel-backend: "none"
disable-network-policy: true
CFGEOF
fi
echo "restarting k3s-$ROLE…"
systemctl restart "k3s${ROLE:+$([ "$ROLE" = agent ] && echo -agent)}" 2>/dev/null \
  || systemctl restart k3s 2>/dev/null || systemctl restart k3s-agent
echo "k3s restarted with flannel disabled. Nodes will be NotReady until Cilium is installed."

cat <<'NEXT'

Next (run ONCE, from a machine with kubectl + helm, after all nodes done):

  helm repo add cilium https://helm.cilium.io && helm repo update
  helm install cilium cilium/cilium -n kube-system \
    --set operator.replicas=1 \
    --set kubeProxyReplacement=false \
    --set hubble.enabled=true \
    --set hubble.relay.enabled=true \
    --set hubble.ui.enabled=true
  # wait for Cilium, then restart workloads so they get Cilium networking:
  kubectl -n kube-system rollout status ds/cilium
  kubectl get pods -A -o name | xargs -r kubectl delete --wait=false

Hubble flow maps: `cilium hubble ui` (or the hubble-ui service). Tetragon
(security-up.sh) already provides process+network detection regardless of CNI.
NEXT
