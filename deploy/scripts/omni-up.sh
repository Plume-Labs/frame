#!/usr/bin/env bash
# omni-up.sh — self-hosted Omni, the bare-metal provisioner for Talos clusters.
#
# Replaces the Sidero Metal path (deploy/sidero/): Sidero Labs no longer
# actively develops Sidero Metal and points at Omni instead.
#
# NOT run on the current test cluster, on purpose. Omni manages Talos machines
# and that cluster is k3s on Ubuntu; there is also no bare metal to provision
# yet. This exists so the path is ready when hardware arrives — see
# deploy/omni/README.md.
#
#   DOMAIN_SUFFIX=lab.example.com \
#   INITIAL_USER=you@example.com \
#   WIREGUARD_ADVERTISED_ENDPOINT=192.168.2.201 \
#   ./deploy/scripts/omni-up.sh
#
# Idempotent: secrets are generated once and reused. Re-running upgrades the
# release without rotating the account identity or the etcd key.
set -euo pipefail

say()  { echo -e "\n\033[1;34m==>\033[0m $*"; }
warn() { echo -e "\033[1;33mwarning:\033[0m $*" >&2; }
die()  { echo -e "\033[1;31merror:\033[0m $*" >&2; exit 1; }

# A single trap: a second `trap ... EXIT` silently replaces the first, and the
# thing that would have been dropped here is a temp GNUPGHOME holding the etcd
# private key.
CLEANUP=()
cleanup() { for path in "${CLEANUP[@]:-}"; do [ -n "$path" ] && rm -rf "$path"; done; }
trap cleanup EXIT

DOMAIN_SUFFIX="${DOMAIN_SUFFIX:?set DOMAIN_SUFFIX — omni/kubernetes/siderolink hostnames hang off it}"
INITIAL_USER="${INITIAL_USER:?set INITIAL_USER — the first admin account, created on first startup only}"
WIREGUARD_ADVERTISED_ENDPOINT="${WIREGUARD_ADVERTISED_ENDPOINT:?set WIREGUARD_ADVERTISED_ENDPOINT — the IP machines reach SideroLink on over UDP}"
CLUSTER_ISSUER="${CLUSTER_ISSUER:-letsencrypt}"
OMNI_STORAGE_CLASS="${OMNI_STORAGE_CLASS:-ceph-rbd}"
CHART_VERSION="${OMNI_CHART_VERSION:?set OMNI_CHART_VERSION — pin the chart, do not float it}"
NS=omni

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALUES_TEMPLATE="${SCRIPT_DIR}/../omni/values.yaml"
[ -f "$VALUES_TEMPLATE" ] || die "missing $VALUES_TEMPLATE"

# ── Preflight ────────────────────────────────────────────────────────────────
for bin in kubectl helm gpg uuidgen envsubst; do
  command -v "$bin" >/dev/null || die "$bin not found in PATH"
done
kubectl cluster-info >/dev/null 2>&1 || die "cannot reach the cluster — check KUBECONFIG"

if ! kubectl get clusterissuer "$CLUSTER_ISSUER" >/dev/null 2>&1; then
  # Not fatal: the release installs, but every ingress stays without a cert and
  # SideroLink enrolment fails on TLS. Better to say so now than to debug it
  # from a machine that will not join.
  warn "ClusterIssuer '$CLUSTER_ISSUER' not found — ingress TLS will not be issued."
  warn "cert-manager is installed on this cluster but no issuer is configured yet."
fi

kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# ── etcd encryption key ──────────────────────────────────────────────────────
# Omni encrypts the machine inventory at rest with this key. Losing it loses the
# inventory, so it is generated once and never rotated by this script.
if kubectl -n "$NS" get secret omni-gpg-key >/dev/null 2>&1; then
  say "etcd encryption key: already present, left alone"
else
  say "etcd encryption key: generating (no passphrase — Omni cannot prompt for one)"
  WORK="$(mktemp -d)"
  CLEANUP+=("$WORK")
  export GNUPGHOME="$WORK/gnupg"
  mkdir -p "$GNUPGHOME"
  chmod 700 "$GNUPGHOME"
  KEY_ID="omni@frame.local"

  gpg --batch --passphrase '' --quick-generate-key \
    "Omni (Used for etcd data encryption) ${KEY_ID}" rsa4096 cert never
  FINGERPRINT="$(gpg --with-colons --list-keys "$KEY_ID" | awk -F: '$1 == "fpr" {print $10; exit}')"
  gpg --batch --passphrase '' --quick-add-key "$FINGERPRINT" rsa4096 encr never
  gpg --export-secret-key --armor "$KEY_ID" > "$WORK/omni.asc"

  kubectl -n "$NS" create secret generic omni-gpg-key --from-file="$WORK/omni.asc"
  warn "BACK UP THIS KEY OUTSIDE THE CLUSTER IT PROTECTS:"
  warn "  kubectl -n $NS get secret omni-gpg-key -o jsonpath='{.data.omni\\.asc}' | base64 -d > omni.asc"
fi

# ── Stable identity + OIDC secret ────────────────────────────────────────────
# The account ID is the install's identity. Regenerating it orphans every
# enrolled machine, so it is stored in-cluster and read back on re-runs.
if kubectl -n "$NS" get secret omni-identity >/dev/null 2>&1; then
  say "account identity: reusing the existing one"
  OMNI_ACCOUNT_ID="$(kubectl -n "$NS" get secret omni-identity -o jsonpath='{.data.account-id}' | base64 -d)"
  OMNI_CLIENT_SECRET="$(kubectl -n "$NS" get secret omni-identity -o jsonpath='{.data.client-secret}' | base64 -d)"
else
  say "account identity: generating"
  OMNI_ACCOUNT_ID="$(uuidgen)"
  OMNI_CLIENT_SECRET="$(head -c 32 /dev/urandom | base64 | tr -d '=+/' | cut -c1-32)"
  kubectl -n "$NS" create secret generic omni-identity \
    --from-literal=account-id="$OMNI_ACCOUNT_ID" \
    --from-literal=client-secret="$OMNI_CLIENT_SECRET"
fi
export OMNI_ACCOUNT_ID OMNI_CLIENT_SECRET DOMAIN_SUFFIX INITIAL_USER \
       WIREGUARD_ADVERTISED_ENDPOINT CLUSTER_ISSUER OMNI_STORAGE_CLASS

# ── Render + sanity-check the values ─────────────────────────────────────────
RENDERED="$(mktemp)"
CLEANUP+=("$RENDERED")
# Substitute ONLY these. Bare `envsubst` replaces every variable it does not
# know with an empty string, so a typo'd placeholder would render as blank and
# the check below would find nothing to complain about. Naming them keeps
# anything unexpected intact, and therefore catchable.
envsubst '${OMNI_ACCOUNT_ID} ${OMNI_CLIENT_SECRET} ${DOMAIN_SUFFIX} ${INITIAL_USER} ${WIREGUARD_ADVERTISED_ENDPOINT} ${CLUSTER_ISSUER} ${OMNI_STORAGE_CLASS}' \
  < "$VALUES_TEMPLATE" > "$RENDERED"

# The advertised WireGuard port and the Service nodePort are two halves of one
# endpoint. Drifting them apart fails silently — machines enrol, then never
# connect — so refuse rather than ship a half-working install.
ADVERTISED_PORT="$(awk -F: '/advertisedEndpoint:/ {gsub(/[^0-9]/, "", $NF); print $NF}' "$RENDERED")"
NODE_PORT="$(awk '/nodePort:/ {gsub(/[^0-9]/, "", $2); print $2}' "$RENDERED")"
[ -n "$ADVERTISED_PORT" ] && [ -n "$NODE_PORT" ] \
  || die "could not read the WireGuard ports out of the rendered values"
[ "$ADVERTISED_PORT" = "$NODE_PORT" ] \
  || die "WireGuard port mismatch: advertisedEndpoint says $ADVERTISED_PORT, nodePort says $NODE_PORT"

if grep -q '\${' "$RENDERED"; then
  die "unsubstituted placeholders remain in the rendered values"
fi

# ── Install ──────────────────────────────────────────────────────────────────
say "omni $CHART_VERSION (namespace $NS, issuer $CLUSTER_ISSUER, storage $OMNI_STORAGE_CLASS)"
helm upgrade --install omni \
  oci://ghcr.io/siderolabs/charts/omni \
  --version "$CHART_VERSION" \
  --namespace "$NS" \
  --values "$RENDERED"

# ── Follow-ups this script cannot do ─────────────────────────────────────────
cat <<EOF

$(say "Remaining steps")

1. DNS — point these at the cluster ingress:
     omni.${DOMAIN_SUFFIX}
     kubernetes.${DOMAIN_SUFFIX}
     siderolink.${DOMAIN_SUFFIX}

2. WireGuard — ${WIREGUARD_ADVERTISED_ENDPOINT}:30180/udp must be reachable
   from every machine Omni will manage. SideroLink is WireGuard: a machine
   behind a NAT that drops UDP will enrol and then never connect, which looks
   like a machine that "joined but is broken".

3. Bare-metal provider (needs IPMI or Redfish on every machine, and a host in
   the machines' own subnet):
     omnictl infraprovider create bare-metal
     docker run -d --name=omni-bare-metal-infra-provider --restart=always \\
       --network host -e OMNI_ENDPOINT -e OMNI_SERVICE_ACCOUNT_KEY \\
       ghcr.io/siderolabs/omni-infra-provider-bare-metal:<version> \\
       --api-advertise-address=<this-host-ip>

4. Machine classes — one boot image per hardware profile, since Omni does not
   classify by PCI vendor the way Sidero did:
     omnictl download iso --arch amd64 --initial-labels frame-role=gpu-worker
     omnictl apply -f deploy/omni/machineclasses/frame-machineclasses.yaml

5. Licence — BSL-1.1 allows self-hosting for non-production only. Settle the
   production question with Sidero before this carries real workloads.
EOF
