#!/bin/bash

set -euo pipefail

echo "🔍 Cluster Health Check"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo ""
echo "📊 Node Status:"
kubectl get nodes -o wide

echo ""
echo "🎯 Control Plane Components:"
kubectl get pods -n kube-system -l tier=control-plane

echo ""
echo "💾 Ceph Cluster Status:"
kubectl -n rook-ceph get cephcluster

echo ""
echo "📦 Storage Classes:"
kubectl get storageclass

echo ""
echo "🌐 RDMA Device Plugin:"
kubectl get daemonset -n kube-system rdma-device-plugin

echo ""
echo "🔌 Network Attachments:"
kubectl get network-attachment-definitions -A

echo ""
echo "📱 Cluster Control UI:"
kubectl get all -n cluster-control

echo ""
echo "🔄 GitOps Status:"
kubectl get gitrepositories,kustomizations -n flux-system

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if kubectl -n rook-ceph get deploy rook-ceph-tools &>/dev/null; then
    echo ""
    echo "🔧 Detailed Ceph Status:"
    kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph status || echo "⚠️  Ceph tools not ready"
fi

echo ""
echo "✅ Health check complete!"
