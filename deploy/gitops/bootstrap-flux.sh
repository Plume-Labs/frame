#!/bin/bash

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-bare-metal-cluster}"
NAMESPACE="${NAMESPACE:-flux-system}"
GITHUB_OWNER="${GITHUB_OWNER:?Set GITHUB_OWNER}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY}"
GITHUB_BRANCH="${GITHUB_BRANCH:-main}"

echo "🚀 Bootstrapping Flux for cluster: ${CLUSTER_NAME}"

flux check --pre

flux bootstrap github \
  --owner="${GITHUB_OWNER}" \
  --repository="${GITHUB_REPOSITORY}" \
  --branch="${GITHUB_BRANCH}" \
  --path="clusters/${CLUSTER_NAME}" \
  --personal

echo "✅ Flux bootstrapped successfully!"
echo "📦 Flux now reconciles whatever Kustomizations live under clusters/${CLUSTER_NAME}"
echo "   (e.g. flux/kustomizations/{ksm-tuner,node-feature-discovery,kmod-rdma-loader}.yaml)."
echo "   cluster-control-ui + the controller-manager are Argo CD-managed now — see"
echo "   ../gitops/argocd/applications/frame.yaml, not Flux."
