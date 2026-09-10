#!/usr/bin/env bash
# argocd-up.sh — install Argo CD for GitOps continuous delivery.
#
# Why: every deploy on this cluster so far (Neura, Frame's own UI) has been
# manual — build, podman save, ssh, ctr import per node, kubectl set image.
# Argo CD watches a git repo and reconciles the cluster to match; `git push`
# replaces that whole dance for anything it manages.
#
# Trimmed footprint for a 3-node test cluster (cp node already sits at ~76%
# memory): no Dex (no SSO — admin password auth is enough here), no
# notifications controller, single replica everywhere. ApplicationSet stays
# (cheap, useful for multiple Applications later).
#
# Config — all optional env vars:
#   ARGOCD_VERSION   chart version   (default: 10.2.1)
#   ARGOCD_NAMESPACE namespace      (default: argocd)
set -euo pipefail

ARGOCD_VERSION="${ARGOCD_VERSION:-10.2.1}"
NS="${ARGOCD_NAMESPACE:-argocd}"

say() { echo -e "\n\033[1;36m==>\033[0m $*"; }

say "Argo CD $ARGOCD_VERSION"
helm repo add argo https://argoproj.github.io/argo-helm >/dev/null 2>&1 || true
helm repo update argo >/dev/null 2>&1 || true

# Les limites ne sont pas decoratives : sans elles le controleur a sature le
# plan de controle deux fois. En aout 2026 (~760m brules a vide) puis le
# 2026-09-10, ou un pic a ete mesure a 976m sur un controleur qui ne
# reconciliait rien — il ne peut pas lire le depot, faute de credentials.
#
# La correction d'aout avait ete posee en `kubectl patch`, donc effacee au
# premier re-run de ce script. C'est pour ca qu'elle vit ici desormais : sur un
# objet gere par Helm, un patch imperatif ne survit pas.
#
# 500m est un plafond, pas un budget : 2 Applications se reconcilient en ~511ms.
helm upgrade --install argocd argo/argo-cd \
  --version "$ARGOCD_VERSION" -n "$NS" --create-namespace \
  --set dex.enabled=false \
  --set notifications.enabled=false \
  --set controller.replicas=1 \
  --set server.replicas=1 \
  --set repoServer.replicas=1 \
  --set redis.enabled=true \
  --set redis-ha.enabled=false \
  --set controller.resources.requests.cpu=100m \
  --set controller.resources.requests.memory=256Mi \
  --set controller.resources.limits.cpu=500m \
  --set controller.resources.limits.memory=1Gi \
  --set repoServer.resources.requests.cpu=50m \
  --set repoServer.resources.requests.memory=128Mi \
  --set repoServer.resources.limits.cpu=500m \
  --set repoServer.resources.limits.memory=512Mi \
  --set server.resources.requests.cpu=50m \
  --set server.resources.requests.memory=128Mi \
  --set server.resources.limits.cpu=300m \
  --set server.resources.limits.memory=512Mi

kubectl -n "$NS" rollout status deploy/argocd-server --timeout=180s
kubectl -n "$NS" rollout status deploy/argocd-repo-server --timeout=180s
kubectl -n "$NS" rollout status statefulset/argocd-application-controller --timeout=180s

say "Initial admin password:"
kubectl -n "$NS" get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d
echo
say "Port-forward to reach the UI: kubectl -n $NS port-forward svc/argocd-server 8080:443"
