#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"

echo "🚀 Starting bare metal cluster bootstrap..."

if [ ! -f "$DEPLOY_DIR/ansible/inventory/production.yml" ]; then
    echo "❌ Error: Inventory file not found at $DEPLOY_DIR/ansible/inventory/production.yml"
    exit 1
fi

echo "📋 Step 1: Validating prerequisites..."
command -v ansible-playbook >/dev/null 2>&1 || { echo "❌ ansible-playbook is required but not installed."; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "❌ kubectl is required but not installed."; exit 1; }

echo "✅ Prerequisites validated"

echo "📋 Step 2: Testing connectivity to all nodes..."
ansible all -i "$DEPLOY_DIR/ansible/inventory/production.yml" -m ping || {
    echo "❌ Failed to reach all nodes. Check your inventory and network connectivity."
    exit 1
}

echo "✅ All nodes reachable"

echo "📋 Step 3: Setting up PXE boot server..."
ansible-playbook -i "$DEPLOY_DIR/ansible/inventory/production.yml" \
    "$DEPLOY_DIR/ansible/playbooks/pxe-bootstrap.yml"

echo "✅ PXE boot server configured"

echo "📋 Step 4: Deploying Kubernetes cluster..."
ansible-playbook -i "$DEPLOY_DIR/ansible/inventory/production.yml" \
    "$DEPLOY_DIR/ansible/playbooks/k8s-cluster.yml"

echo "✅ Kubernetes cluster deployed"

echo "📋 Step 5: Configuring kubectl access..."
CONTROL_NODE=$(grep -A 1 "control_plane:" "$DEPLOY_DIR/ansible/inventory/production.yml" | tail -1 | awk '{print $1}' | tr -d ':')
CONTROL_IP=$(grep -A 2 "$CONTROL_NODE:" "$DEPLOY_DIR/ansible/inventory/production.yml" | grep ansible_host | awk '{print $2}')

echo "Fetching kubeconfig from $CONTROL_NODE ($CONTROL_IP)..."
ssh root@"$CONTROL_IP" "cat /etc/kubernetes/admin.conf" > "$HOME/.kube/config-bare-metal"
export KUBECONFIG="$HOME/.kube/config-bare-metal"

echo "✅ kubectl configured"

echo "📋 Step 6: Deploying RDMA networking..."
kubectl apply -k "$DEPLOY_DIR/networking/"

echo "✅ RDMA networking configured"

echo "📋 Step 7: Deploying Ceph storage..."
kubectl create namespace rook-ceph || true
kubectl apply -f https://raw.githubusercontent.com/rook/rook/master/deploy/examples/crds.yaml
kubectl apply -f https://raw.githubusercontent.com/rook/rook/master/deploy/examples/common.yaml
kubectl apply -f https://raw.githubusercontent.com/rook/rook/master/deploy/examples/operator.yaml
sleep 30
kubectl apply -k "$DEPLOY_DIR/ceph/"

echo "✅ Ceph storage deployed"

echo "📋 Step 8: Waiting for Ceph cluster to be ready..."
kubectl -n rook-ceph wait --for=condition=Ready cephcluster/rook-ceph --timeout=600s || {
    echo "⚠️  Ceph cluster not ready yet. Check status with: kubectl -n rook-ceph get cephcluster"
}

echo "📋 Step 9: Initializing GitOps with Flux..."
cd "$DEPLOY_DIR/gitops"
./bootstrap-flux.sh

echo "✅ GitOps initialized"

echo "📋 Step 10: Deploying Cluster Control UI..."
kubectl apply -k "$DEPLOY_DIR/kubernetes/overlays/production/"

echo "✅ Cluster Control UI deployed"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🎉 Bootstrap complete!"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📊 Cluster Status:"
kubectl get nodes -o wide
echo ""
echo "📦 Deployed Resources:"
kubectl get all -n cluster-control
echo ""
echo "🔍 Check Ceph status:"
echo "  kubectl -n rook-ceph get cephcluster"
echo "  kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph status"
echo ""
echo "🌐 Access Cluster Control UI:"
echo "  kubectl port-forward -n cluster-control svc/cluster-control-ui 8080:80"
echo "  Then visit: http://localhost:8080"
echo ""
echo "📝 Export kubeconfig:"
echo "  export KUBECONFIG=$HOME/.kube/config-bare-metal"
echo ""
