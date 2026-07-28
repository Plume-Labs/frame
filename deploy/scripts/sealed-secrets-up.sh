#!/usr/bin/env bash
# sealed-secrets-up.sh — install the sealed-secrets controller, which lets app
# repos commit encrypted Secret values instead of plaintext.
#
# Why: k8s/helm/neura/values.local.yaml (a tracked, non-gitignored file) had
# real values in secrets.values (DB_PASSWORD, JWT_SECRET) sitting in plaintext
# in git history. SealedSecret CRs are encrypted against this controller's
# public key — only this specific controller instance (private key never
# leaves the cluster) can decrypt them back into a regular Secret. Safe to
# commit; useless to anyone without cluster access.
#
# Installed from the project's own release manifest (kubectl apply), not a
# Helm chart — bitnami-labs.github.io/sealed-secrets (the old chart repo) 404s;
# the project moved to github.com/bitnami/sealed-secrets and ships this as the
# documented install path.
#
# Config — all optional env vars:
#   SS_VERSION     controller version         (default: v0.38.4)
#   SS_NAMESPACE   namespace it installs into (fixed by the upstream manifest: kube-system)
set -euo pipefail

SS_VERSION="${SS_VERSION:-v0.38.4}"
NS=kube-system

say() { echo -e "\n\033[1;36m==>\033[0m $*"; }

say "sealed-secrets controller $SS_VERSION"
kubectl apply -f "https://github.com/bitnami/sealed-secrets/releases/download/${SS_VERSION}/controller.yaml"

kubectl -n "$NS" rollout status deploy/sealed-secrets-controller --timeout=180s

say "Done. Encrypt a value with:"
echo "  kubeseal --controller-namespace=$NS --controller-name=sealed-secrets-controller --raw --scope namespace-wide --name <secret-name> --namespace <ns> <<< '<value>'"
