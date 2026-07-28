#!/usr/bin/env bash
# postgres-operator-up.sh — install the Zalando postgres-operator, which manages
# Patroni-based Postgres clusters declared as `acid.zalan.do/v1 postgresql` CRs.
#
# Why an operator at all: the hand-rolled StatefulSet this replaces mounted its
# PVC at the vanilla `/var/lib/postgresql/data` while the Spilo-derived
# timescaledb-ha image keeps PGDATA at `/home/postgres/pgdata/data`. The database
# therefore lived on the container's ephemeral overlay — lost on every restart —
# and Velero's fs-backup dutifully archived the empty PVC and reported success.
# The operator owns that layout, so it cannot drift again.
#
# Version pin: 2.0.0 is the first release supporting Postgres 18, which is what
# the existing data is. 1.15.1 caps at 17 and its CRD rejects version "18"
# outright. 2.0.0 is a major release, but its breaking changes concern migrating
# *existing* 1.x clusters (docs/migrate.md) — a fresh install skips all of it.
#
# Config — all optional env vars:
#   PGO_VERSION      operator chart version         (default: 2.0.0)
#   PGO_NAMESPACE    namespace to install into      (default: postgres-operator)
set -euo pipefail

PGO_VERSION="${PGO_VERSION:-2.0.0}"
NS="${PGO_NAMESPACE:-postgres-operator}"

say() { echo -e "\n\033[1;36m==>\033[0m $*"; }

say "Zalando postgres-operator $PGO_VERSION"
helm repo add postgres-operator-charts \
  https://opensource.zalando.com/postgres-operator/charts/postgres-operator >/dev/null 2>&1 || true
helm repo update postgres-operator-charts >/dev/null 2>&1 || true

# watch_the_whole_cluster: the operator reconciles postgresql CRs in every
# namespace, so Neura can declare its own cluster inside the `neura` namespace
# without the operator chart having to know about it.
helm upgrade --install postgres-operator \
  postgres-operator-charts/postgres-operator \
  --version "$PGO_VERSION" -n "$NS" --create-namespace \
  --set configKubernetes.enable_cross_namespace_secret=true \
  --set configGeneral.workers=2

kubectl -n "$NS" rollout status deploy/postgres-operator --timeout=180s

say "Done. Declare clusters with acid.zalan.do/v1 postgresql (see the Neura chart)."
