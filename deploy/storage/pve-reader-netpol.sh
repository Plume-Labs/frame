#!/usr/bin/env bash
# Prints the NetworkPolicy that fences the Proxmox reader (deploy/storage/
# pve-reader.yaml). Reads the cluster (kubectl get nodes, read-only); writes
# nothing. Review the output, then apply it:
#
#   PVE_HOST=<proxmox address> deploy/storage/pve-reader-netpol.sh | kubectl apply -f -
#
# Ingress: the console reaches the reader only through the apiserver's pod
# proxy (console → uiproxy → apiserver → pod), so the one legitimate caller
# is the apiserver, which runs on the control-plane nodes. Only their
# addresses are allowed on 8080: the node address, and — with flannel — the
# cni0 bridge (.1 of the node's pod CIDR) and the flannel.1 VXLAN interface
# (.0) the node's traffic to a pod leaves from.
#
# What this does NOT stop, stated rather than implied:
#   - a pod in hostNetwork on a control-plane node shares that node's address
#     and passes;
#   - kube-router (k3s's NetworkPolicy engine) accepts traffic local to the
#     pod's own node regardless of policy — that is also why the kubelet's
#     readiness probe works without a rule here — so a hostNetwork pod on the
#     reader's node passes too.
# Ordinary pods, the console's own included, are refused: they cannot call
# the reader directly and borrow its token.
#
# Egress: the Proxmox API (PVE_HOST:8006) only. PVE_HOST must be an IPv4
# address here: a name would need DNS egress as well, and an ipBlock cannot
# hold a name.
#
# The addresses are site-specific, which is why this is generated rather than
# committed: the public repository carries the rule, not the addresses.
set -euo pipefail

: "${PVE_HOST:?set PVE_HOST to the Proxmox API address (IPv4)}"
if ! [[ "$PVE_HOST" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  echo "PVE_HOST must be an IPv4 address, got: $PVE_HOST" >&2
  exit 1
fi

nodes="$(kubectl get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address} {.spec.podCIDR}{"\n"}{end}')"
if [[ -z "$nodes" ]]; then
  echo "no control-plane node read from the cluster (label node-role.kubernetes.io/control-plane)" >&2
  exit 1
fi

sources=()
while read -r ip cidr; do
  [[ -n "$ip" ]] && sources+=("$ip")
  if [[ "$cidr" =~ ^([0-9]+\.[0-9]+\.[0-9]+)\.0/[0-9]+$ ]]; then
    sources+=("${BASH_REMATCH[1]}.0" "${BASH_REMATCH[1]}.1")
  elif [[ -n "$cidr" ]]; then
    echo "pod CIDR $cidr is not a .0 network this script understands" >&2
    exit 1
  fi
done <<<"$nodes"

cat <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: frame-pve-reader
  namespace: frame-pve-reader
  labels:
    app.kubernetes.io/part-of: cluster-control-containment
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: frame-pve-reader
  policyTypes: [Ingress, Egress]
  ingress:
    - ports:
        - protocol: TCP
          port: 8080
      from:
EOF
for s in $(printf '%s\n' "${sources[@]}" | sort -u); do
  printf '        - ipBlock:\n            cidr: %s/32\n' "$s"
done
cat <<EOF
  egress:
    - ports:
        - protocol: TCP
          port: 8006
      to:
        - ipBlock:
            cidr: ${PVE_HOST}/32
EOF
