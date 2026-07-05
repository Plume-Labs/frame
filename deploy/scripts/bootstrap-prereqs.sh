#!/usr/bin/env bash
# Install cluster prerequisites for Frame.
# Run once against a fresh cluster before: kubectl apply -k config/default
#
# Usage: ./bootstrap-prereqs.sh [--skip-volcano]
set -euo pipefail

SKIP_VOLCANO=false
for arg in "$@"; do
  [[ "$arg" == "--skip-volcano" ]] && SKIP_VOLCANO=true
done

for bin in kubectl helm; do
  command -v "$bin" >/dev/null 2>&1 || { echo "❌ Missing: $bin"; exit 1; }
done

echo "▶ cert-manager"
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.0/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=Available deployment --all --timeout=120s

echo "▶ ingress-nginx"
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx --force-update
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.service.type=NodePort \
  --wait --timeout 120s
  # Note: use LoadBalancer if MetalLB or kube-vip is installed.

echo "▶ Argo Workflows"
kubectl create namespace argo --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argo -f https://github.com/argoproj/argo-workflows/releases/download/v3.6.7/install.yaml
kubectl -n argo wait --for=condition=Available deployment --all --timeout=120s

if [[ "$SKIP_VOLCANO" == "false" ]]; then
  echo "▶ Volcano (gang-scheduling)"
  helm repo add volcano-sh https://volcano-sh.github.io/helm-charts --force-update
  helm upgrade --install volcano volcano-sh/volcano \
    --namespace volcano-system --create-namespace \
    --wait --timeout 120s
fi

echo "✅ Prerequisites installed. Next: kubectl apply -k config/default"
