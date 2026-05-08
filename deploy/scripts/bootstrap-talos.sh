#!/usr/bin/env bash
# Bootstrap a Talos cluster for Frame
# Replaces the Ansible k8s-cluster.yml workflow
#
# Prerequisites: talosctl, kubectl, flux CLI installed
#
# Usage: ./bootstrap-talos.sh <controlplane-ip> <worker-ips-comma-separated>

set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <controlplane-ip> <worker-ips-comma-separated>"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CP_IP="$1"
WORKER_IPS="$2"
CLUSTER_NAME="${CLUSTER_NAME:-frame-cluster}"
CONFIG_DIR="${TALOS_CONFIG_DIR:-${REPO_ROOT}/deploy/talos/generated}"
GITHUB_OWNER="${GITHUB_OWNER:-${GITHUB_USER:-}}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-${GITHUB_REPO:-}}"

for bin in talosctl kubectl flux; do
  command -v "$bin" >/dev/null 2>&1 || {
    echo "❌ Missing required binary: $bin"
    exit 1
  }
done

mkdir -p "${CONFIG_DIR}"

echo "📦 Generating Talos configs into ${CONFIG_DIR}"
talosctl gen config "${CLUSTER_NAME}" "https://${CP_IP}:6443" \
  --output-dir "${CONFIG_DIR}" \
  --config-patch-control-plane @"${REPO_ROOT}/deploy/talos/patches/numa-irq.yaml" \
  --config-patch-worker @"${REPO_ROOT}/deploy/talos/patches/kubelet-config.yaml" \
  --config-patch-worker @"${REPO_ROOT}/deploy/talos/patches/hugepages.yaml" \
  --config-patch-worker @"${REPO_ROOT}/deploy/talos/patches/numa-irq.yaml"

echo "🛠️ Applying control plane config to ${CP_IP}"
talosctl apply-config --insecure --nodes "${CP_IP}" --file "${CONFIG_DIR}/controlplane.yaml"

echo "🛠️ Applying worker config to ${WORKER_IPS}"
talosctl apply-config --insecure --nodes "${WORKER_IPS}" --file "${CONFIG_DIR}/worker.yaml"

echo "🚀 Bootstrapping Talos control plane"
talosctl bootstrap --nodes "${CP_IP}" || true

echo "🔐 Fetching kubeconfig"
(
  cd "${CONFIG_DIR}"
  talosctl kubeconfig .
)
export KUBECONFIG="${CONFIG_DIR}/kubeconfig"

echo "🔄 Bootstrapping Flux from environment variables"
: "${GITHUB_OWNER:?GITHUB_OWNER (or GITHUB_USER) is required for flux bootstrap github}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY (or GITHUB_REPO) is required for flux bootstrap github}"
flux bootstrap github \
  --owner="${GITHUB_OWNER}" \
  --repository="${GITHUB_REPOSITORY}" \
  --branch="${GITHUB_BRANCH:-main}" \
  --path="${GITOPS_PATH:-clusters/${CLUSTER_NAME}}"

echo "✅ Talos bootstrap completed. Re-running this script is safe: Talos apply operations are declarative and Flux bootstrap is idempotent."
