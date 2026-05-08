#!/usr/bin/env bash

set -euo pipefail

NODE_NAME="${1:-}"
NODE_TYPE="${3:-worker}"

if [ -z "$NODE_NAME" ]; then
  echo "Usage: $0 <node-name> [node-ip] [node-type]"
  echo "Example: $0 worker-05 192.168.1.25 worker"
  exit 1
fi

cat <<EOF
❌ Ansible-based hot-add has been removed.

Use the Talos + Sidero flow instead:
1. Register the server in your Sidero inventory (BMC + PXE environment).
2. Assign an appropriate ServerClass in /home/runner/work/frame/frame/deploy/sidero/serverclasses/.
3. Reconcile via Flux and verify:
   kubectl get machines -A
   kubectl get nodes

Requested node metadata:
- name: ${NODE_NAME}
- type: ${NODE_TYPE}
EOF
exit 1
