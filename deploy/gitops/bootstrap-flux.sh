#!/bin/bash

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-bare-metal-cluster}"
NAMESPACE="${NAMESPACE:-flux-system}"
GITHUB_USER="${GITHUB_USER:-your-org}"
GITHUB_REPO="${GITHUB_REPO:-cluster-gitops}"
GITHUB_BRANCH="${GITHUB_BRANCH:-main}"

echo "🚀 Bootstrapping Flux for cluster: ${CLUSTER_NAME}"

flux check --pre

flux bootstrap github \
  --owner="${GITHUB_USER}" \
  --repository="${GITHUB_REPO}" \
  --branch="${GITHUB_BRANCH}" \
  --path="clusters/${CLUSTER_NAME}" \
  --personal

echo "✅ Flux bootstrapped successfully!"
echo "📦 Applying GitOps resources..."

kubectl apply -k ../kubernetes/overlays/production/

echo "✅ Deployment complete!"
