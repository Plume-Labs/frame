#!/usr/bin/env bash
# Prints the NetworkPolicy that fences the Proxmox reader (deploy/storage/
# pve-reader.yaml). Reads the cluster (kubectl get nodes, read-only); writes
# nothing. Review the output, then apply it:
#
#   PVE_HOST=<proxmox address> deploy/storage/pve-reader-netpol.sh | kubectl apply -f -
#
# Ingress: the console reaches the reader only through the apiserver's pod
# proxy (console → uiproxy → apiserver → pod), so the only legitimate TCP
# source is a node — the apiserver on its host network, and the kubelet for
# the readiness probe. With flannel, a node reaching a pod leaves from its
# node address, its cni0 bridge (.1 of its pod CIDR) or its flannel.1 VXLAN
# interface (.0 of its pod CIDR). Those /32s are allowed on 8080; every pod
# address is not, so no workload — the console's own pods included — can call
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

nodes="$(kubectl get nodes -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address} {.spec.podCIDR}{"\n"}{end}')"
if [[ -z "$nodes" ]]; then
  echo "no node read from the cluster" >&2
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
