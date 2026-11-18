#!/usr/bin/env bash
# Upgrade e2e: install the operator as it was (N-1), upgrade it to this tree
# (N), then roll it back to N-1, on a throwaway Kind cluster, checking at each
# step that what the operator already stored survives.
#
#   hack/upgrade-e2e.sh            # N-1 = merge-base with origin/main (or HEAD~1)
#   UPGRADE_FROM=<git ref> hack/upgrade-e2e.sh
#
# What it proves, per step: the operator rolls out and does not restart; the
# stored objects (4 FrameNodes, 4 SchedulingPolicies, 3 FrameJobs, the same
# fixtures as `make demo`) keep their spec byte for byte (no CRD pruning, no
# defaulting drift) and their status; the admission webhook and the CRD schema
# still accept a new object; and, after the rollback, N-1 reconciles new work (the jobs are
# deleted and submitted again).
#
# Scope: the operator (frame-controller + its CRDs, RBAC and webhook, from
# `config/default`). frame-provisiond, authd and the console are not part of
# it; the provisiond Pod stays ImagePullBackOff here and is ignored.
#
# Needs: docker, kind, kubectl, go (kustomize), git, jq, curl. The cluster has
# its own kubeconfig in a temp directory: ~/.kube/config is neither read nor
# changed.
set -euo pipefail
umask 077

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

CLUSTER="${UPGRADE_CLUSTER:-hearthrack-upgrade}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.20.2}" # keep in step with test/utils/utils.go
KIND="${KIND:-kind}"

tmp="$(mktemp -d)"
export KUBECONFIG="$tmp/kubeconfig"
prev="$tmp/prev"
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then dump || true; fi
  git -C "$root" worktree remove --force "$prev" >/dev/null 2>&1 || true
  if [ -z "${UPGRADE_KEEP_CLUSTER:-}" ]; then "$KIND" delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup EXIT

k() { kubectl "$@"; }
log() { printf '\n==> %s\n' "$*"; }
fail() { echo "upgrade-e2e: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "'$1' is required but not installed"; }
for tool in docker "$KIND" kubectl go git jq curl; do need "$tool"; done

dump() {
  echo "--- cluster state at the failure ---" >&2
  k get pods -A >&2 || true
  k -n frame-system describe deployment frame-controller-manager >&2 || true
  k -n frame-system logs deployment/frame-controller-manager --tail=200 >&2 || true
  k -n frame-system get framenodes,framejobs,schedulingpolicies >&2 || true
}

# wait_for DESCRIPTION CMD...: retry CMD for up to two minutes.
wait_for() {
  local what="$1"
  shift
  for _ in $(seq 1 60); do
    "$@" >/dev/null 2>&1 && return
    sleep 2
  done
  fail "timed out waiting for ${what}"
}

# ---- N-1 and N -------------------------------------------------------------

if [ -n "${UPGRADE_FROM:-}" ]; then
  from="$(git rev-parse --verify "${UPGRADE_FROM}^{commit}")"
else
  from="$(git merge-base HEAD origin/main 2>/dev/null || true)"
  if [ -z "$from" ] || [ "$from" = "$(git rev-parse HEAD)" ]; then from="$(git rev-parse HEAD~1)"; fi
fi
log "N-1 = $(git log -1 --format='%h %s' "$from")"
log "N   = $(git log -1 --format='%h %s' HEAD) (this tree, with its local changes)"
git worktree add --detach "$prev" "$from" >/dev/null

IMG_PREV="frame-controller:upgrade-prev"
IMG_NEXT="frame-controller:upgrade-next"
log "Building both controller images"
docker build -q -f "$prev/Dockerfile.controller" -t "$IMG_PREV" "$prev"
docker build -q -f Dockerfile.controller -t "$IMG_NEXT" .

make -s kustomize
KUSTOMIZE="$root/bin/kustomize"
export KUSTOMIZE

# deploy TREE IMAGE: what `make deploy DEPLOY_DEFAULT=1` does, from that tree's
# own config/, so each version installs exactly the manifests it shipped.
deploy() {
  "$1/hack/render-overlay.sh" default "$2" | k apply -f - >/dev/null
  k -n frame-system rollout status deployment/frame-controller-manager --timeout=240s
}

# ---- cluster ---------------------------------------------------------------

log "Creating Kind cluster '$CLUSTER'"
"$KIND" delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
"$KIND" create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --config hack/demo/kind.yaml
"$KIND" load docker-image --name "$CLUSTER" "$IMG_PREV" "$IMG_NEXT"

log "Installing cert-manager ${CERT_MANAGER_VERSION}"
k apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" >/dev/null
for d in cert-manager cert-manager-cainjector cert-manager-webhook; do
  k -n cert-manager rollout status "deployment/$d" --timeout=180s
done

# ---- fixtures and checks ---------------------------------------------------

job_phase() {
  [ "$(k -n frame-system get framejob "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}')" = "$2" ]
}
nodes_online() {
  [ "$(k -n frame-system get framenodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].reason}{"\n"}{end}' | grep -c '^Online$')" = "4" ]
}
jobs_settled() {
  job_phase demo-nightly-report Completed && job_phase demo-training-run Running && job_phase demo-flaky-etl Failed
}

# apply_retry FILE...: the rollout reports the operator Ready a moment before
# its webhook Service routes to it, so the first apply can be refused.
apply_retry() {
  for _ in $(seq 1 30); do
    if sed "s#CLUSTER_NAME#${CLUSTER}#g" "$@" | k apply -f - >/dev/null 2>"$tmp/apply.err"; then return; fi
    sleep 2
  done
  cat "$tmp/apply.err" >&2
  fail "could not apply $*"
}

# snapshot: the spec of every fixture object, one stable JSON line each.
snapshot() {
  for kind in framenodes schedulingpolicies framejobs; do
    k -n frame-system get "$kind" -o json | jq -cS '.items[] | {kind, name: .metadata.name, spec}'
  done | sort
}

# check STEP: the operator is healthy and nothing stored has changed.
check() {
  log "Checking after: $1"
  k -n frame-system rollout status deployment/frame-controller-manager --timeout=120s
  restarts="$(k -n frame-system get pods -l control-plane=controller-manager -o json | jq '[.items[].status.containerStatuses[]?.restartCount] | add // 0')"
  [ "$restarts" = "0" ] || fail "the manager restarted ${restarts} time(s) after: $1"
  wait_for "four nodes Online" nodes_online
  wait_for "jobs Completed, Running, Failed" jobs_settled
  for pc in frame-critical frame-high frame-medium frame-low; do k get priorityclass "$pc" >/dev/null || fail "PriorityClass $pc is gone after: $1"; done
  snapshot >"$tmp/now.json"
  if ! diff -u "$tmp/baseline.json" "$tmp/now.json"; then fail "stored specs changed after: $1"; fi
  # The webhook is in the path of every write: a server-side dry run exercises it.
  sed "s#CLUSTER_NAME#${CLUSTER}#g" hack/demo/seed.yaml | k apply --dry-run=server -f - >/dev/null || fail "the admission webhook refused a valid object after: $1"
  # An update of a stored object is not enough: CRD validation ratcheting lets
  # an object that was valid before keep passing, so a new required field got
  # through when only existing objects were applied (shown by a deliberately
  # broken PR). A CREATE is validated against the whole schema.
  probe='{"apiVersion":"frame.plume-labs.io/v1beta1","kind":"FrameNode","metadata":{"name":"upgrade-probe","namespace":"frame-system"},"spec":{"hostname":"upgrade-probe","ip":"192.0.2.99","role":"worker","rack":"rack-a","zone":"zone-1","serviceClass":"HIGH"}}'
  printf '%s' "$probe" | k create --dry-run=server -f - >/dev/null || fail "a new FrameNode is refused after: $1 (CRD schema or webhook break)"
}

# ---- N-1 -------------------------------------------------------------------

log "Installing N-1"
deploy "$prev" "$IMG_PREV"
apply_retry hack/demo/seed.yaml
wait_for "four nodes Online" nodes_online
apply_retry hack/demo/workloads.yaml
for pc in frame-critical frame-high frame-medium frame-low; do wait_for "PriorityClass $pc" k get priorityclass "$pc"; done
apply_retry hack/demo/jobs.yaml
wait_for "jobs Completed, Running, Failed" jobs_settled
snapshot >"$tmp/baseline.json"
[ -s "$tmp/baseline.json" ] || fail "the baseline snapshot is empty"
echo "baseline: $(wc -l <"$tmp/baseline.json") stored objects"

# ---- N-1 -> N --------------------------------------------------------------

log "Upgrading to N"
deploy "$root" "$IMG_NEXT"
check "the upgrade to N"

# ---- N -> N-1 --------------------------------------------------------------

log "Rolling back to N-1"
deploy "$prev" "$IMG_PREV"
check "the rollback to N-1"

# N-1 must still do new work, not only keep what it finds.
log "Submitting the jobs again under N-1"
k -n frame-system delete framejobs --all --wait=true >/dev/null
apply_retry hack/demo/jobs.yaml
wait_for "jobs Completed, Running, Failed again" jobs_settled

log "Upgrade e2e passed: N-1 -> N -> N-1"
