#!/usr/bin/env bash

set -euo pipefail

NODE_NAME="${1:-}"
NODE_TYPE="${2:-worker}"

if [ -z "$NODE_NAME" ]; then
  echo "Usage: $0 <node-name> [node-type]"
  echo "Example: $0 worker-05 worker"
  exit 1
fi

cat <<EOF
❌ Ansible-based hot-add has been removed.

Use the Talos + Omni flow instead (Sidero Metal is no longer developed
upstream — see deploy/omni/README.md):
1. Register the machine's BMC with the bare-metal infrastructure provider.
2. Boot it from the image carrying the right role label, e.g.
     omnictl download iso --arch amd64 --initial-labels frame-role=<role>
   The role comes from the image, not from the hardware, so booting the
   wrong image silently joins the machine to the wrong class.
3. Verify:
   omnictl get machines
   kubectl get nodes

Requested node metadata:
- name: ${NODE_NAME}
- type: ${NODE_TYPE}
EOF
exit 1
