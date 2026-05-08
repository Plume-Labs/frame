#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CP_IP="${1:-${CONTROLPLANE_IP:-}}"
WORKER_IPS="${2:-${WORKER_IPS:-}}"

if [ -z "${CP_IP}" ] || [ -z "${WORKER_IPS}" ]; then
  echo "Usage: $0 <controlplane-ip> <worker-ips-comma-separated>"
  echo "Or set CONTROLPLANE_IP and WORKER_IPS environment variables."
  exit 1
fi

echo "ℹ️  bootstrap-cluster.sh now delegates to the Talos-native bootstrap flow."
exec "${SCRIPT_DIR}/bootstrap-talos.sh" "${CP_IP}" "${WORKER_IPS}"
