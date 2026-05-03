#!/bin/bash

set -euo pipefail

NODE_NAME="${1:-}"
NODE_IP="${2:-}"
NODE_TYPE="${3:-worker}"

if [ -z "$NODE_NAME" ] || [ -z "$NODE_IP" ]; then
    echo "Usage: $0 <node-name> <node-ip> [node-type]"
    echo "Example: $0 worker-05 192.168.1.25 worker"
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"

echo "🔥 Hot adding node: $NODE_NAME ($NODE_IP) as $NODE_TYPE"

export NEW_NODE_NAME="$NODE_NAME"
export NEW_NODE_IP="$NODE_IP"
export NODE_TYPE="$NODE_TYPE"

ansible-playbook -i "$DEPLOY_DIR/ansible/inventory/production.yml" \
    "$DEPLOY_DIR/ansible/playbooks/hot-add-node.yml" \
    -e "new_node_hostname=$NODE_NAME" \
    -e "new_node_ip=$NODE_IP" \
    -e "node_type=$NODE_TYPE"

echo "✅ Node $NODE_NAME added successfully!"
echo ""
echo "📊 Node Status:"
kubectl get node "$NODE_NAME"
echo ""
echo "🏷️  Node Labels:"
kubectl get node "$NODE_NAME" --show-labels
echo ""
