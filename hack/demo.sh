#!/usr/bin/env bash
# Hearthrack demo: a throwaway Kind cluster with the whole stack installed
# (operator, authd, console), so the console can be tried without any hardware.
#
#   hack/demo.sh up       build the images, create the cluster, install, bootstrap the first admin
#   hack/demo.sh verify   CI check: sign in with the one-shot token, read through the console
#                         (uses up the token: do not run it if you want to sign in by hand)
#   hack/demo.sh down     delete the cluster
#
# Needs: docker, kind, kubectl, go (make deploy renders config/ with kustomize),
# curl, openssl. Nothing here touches a cluster other than the Kind one it
# creates: the cluster gets its own kubeconfig ($STATE_DIR/kubeconfig, exported
# as KUBECONFIG for every command below, make included), so neither your
# ~/.kube/config nor your current context is read or changed.
#
# The demo cluster has four real nodes (one control plane, three workers),
# classified into two racks by hack/demo/seed.yaml. Jobs, volumes and alerts are
# not seeded yet. See docs/known-limitations.md.
set -euo pipefail
umask 077

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

CLUSTER="${DEMO_CLUSTER:-hearthrack-demo}"
# Node names are <cluster>-control-plane etc., used as FrameNode spec.hostname
# (a DNS-1123 label, 63 characters at most).
case "$CLUSTER" in
  *[!a-z0-9-]*|-*|*-|"") echo "demo: DEMO_CLUSTER must be lowercase letters, digits and '-'" >&2; exit 2 ;;
esac
[ "${#CLUSTER}" -le 49 ] || { echo "demo: DEMO_CLUSTER is too long (49 characters at most)" >&2; exit 2; }
PORT="${DEMO_PORT:-18080}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.20.2}" # keep in step with test/utils/utils.go
KIND="${KIND:-kind}"
ADMIN_EMAIL="${DEMO_ADMIN_EMAIL:-admin@example.com}"
STATE_DIR="${DEMO_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/hearthrack-demo}"
MARKER="$STATE_DIR/.hearthrack-demo"
export KUBECONFIG="$STATE_DIR/kubeconfig"
tmp=""
trap '[ -z "$tmp" ] || rm -rf "$tmp"' EXIT

IMG_CONTROLLER="frame-controller:demo"
IMG_UI="cluster-control:demo"
IMG_UIPROXY="frame-uiproxy:demo"
IMG_AUTHD="cluster-control-auth:demo"
# config/provisiond names this tag and IfNotPresent: it must be in the node.
IMG_PROVISIOND="frame-provisiond:latest"

k() { kubectl "$@"; }
log() { printf '\n==> %s\n' "$*"; }
# The state directory holds a session cookie and the admin bootstrap token:
# ours, private, and never a directory someone else prepared.
init_state() {
  mkdir -p "$STATE_DIR"
  chmod 700 "$STATE_DIR"
  [ -O "$STATE_DIR" ] || { echo "demo: $STATE_DIR is not owned by you" >&2; exit 1; }
  touch "$MARKER"
}
need() { command -v "$1" >/dev/null 2>&1 || { echo "demo: '$1' is required but not installed" >&2; exit 1; }; }

build_images() {
  log "Building images"
  docker build -q -f Dockerfile.controller -t "$IMG_CONTROLLER" .
  docker build -q -t "$IMG_UI" .
  docker build -q -f Dockerfile.uiproxy -t "$IMG_UIPROXY" .
  docker build -q -f Dockerfile.authd -t "$IMG_AUTHD" .
  docker build -q -f Dockerfile.provisiond -t "$IMG_PROVISIOND" .
}

create_cluster() {
  if "$KIND" get clusters | grep -qx "$CLUSTER"; then
    log "Kind cluster '$CLUSTER' already exists, reusing it"
    "$KIND" get kubeconfig --name "$CLUSTER" >"$KUBECONFIG"
    # kind.yaml is not re-applied to an existing cluster: refuse one that
    # predates the four-node demo rather than fail two minutes later.
    nodes="$(kubectl get nodes --no-headers | wc -l)"
    [ "$nodes" = "4" ] || { echo "demo: cluster '$CLUSTER' has $nodes node(s), created before the four-node demo: run 'hack/demo.sh down' then 'up'" >&2; exit 1; }
  else
    log "Creating Kind cluster '$CLUSTER'"
    "$KIND" create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --config hack/demo/kind.yaml
  fi
  "$KIND" load docker-image --name "$CLUSTER" "$IMG_CONTROLLER" "$IMG_UI" "$IMG_UIPROXY" "$IMG_AUTHD" "$IMG_PROVISIOND"
}

install_cert_manager() {
  log "Installing cert-manager ${CERT_MANAGER_VERSION}"
  k apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
  for d in cert-manager cert-manager-cainjector cert-manager-webhook; do
    k -n cert-manager rollout status "deployment/$d" --timeout=180s
  done
}

install_operator() {
  log "Installing the operator (config/default)"
  # The FrameJob controller watches Argo Workflows. Without that CRD its watch
  # never syncs: the controller does not start (no FrameJob is ever reconciled)
  # and, after the two-minute cache-sync timeout, the whole manager exits. The
  # demo has no Argo, so it installs only the stand-in CRD the unit tests use
  # (container jobs never create a Workflow; pipeline jobs would not run).
  k apply -f internal/controller/frame/testdata/crds/argoproj.io_workflows.yaml
  # make deploy reads KUBECONFIG, exported above: it can only reach this cluster.
  make deploy DEPLOY_DEFAULT=1 IMG="$IMG_CONTROLLER"
  k -n frame-system rollout status deployment/frame-controller-manager --timeout=180s
}

# Classify the demo cluster's nodes (hack/demo/seed.yaml) and wait until the
# operator has projected the labels: that is what the Nodes screens read.
seed() {
  log "Classifying the demo nodes"
  # The rollout reports the operator Ready a moment before its webhook Service
  # routes to it ("connection refused"), so the first apply can race it: retry
  # for up to a minute rather than fail a demo that is about to work.
  seeded=""
  for _ in $(seq 1 30); do
    if sed "s#CLUSTER_NAME#${CLUSTER}#g" hack/demo/seed.yaml | k apply -f - 2>/dev/null; then
      seeded=1
      break
    fi
    sleep 2
  done
  if [ -z "$seeded" ]; then
    echo "demo: could not create the demo nodes (is the operator webhook up?):" >&2
    sed "s#CLUSTER_NAME#${CLUSTER}#g" hack/demo/seed.yaml | k apply -f - >&2 || true
    exit 1
  fi
  for _ in $(seq 1 60); do
    n="$(k -n frame-system get framenodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].reason}{"\n"}{end}' 2>/dev/null | grep -c '^Online$' || true)"
    [ "$n" = "4" ] && return
    sleep 2
  done
  echo "demo: the four demo nodes did not all reach Online:" >&2
  k -n frame-system get framenodes >&2 || true
  exit 1
}

# Jobs, volumes, alerts and the scheduling tiers: real objects of the kinds the
# console already reads (hack/demo/*.yaml, all labelled hearthrack.demo/seeded),
# so no screen is empty and nothing here is a console-side mock. Waits for the
# end state it seeded, so `up` fails here rather than leaving a half-populated demo.
seed_workloads() {
  log "Seeding jobs, volumes and alerts"
  k apply -f hack/demo/workloads.yaml
  # A pod naming a PriorityClass that does not exist is rejected, so the
  # operator must have reconciled the four tiers before any job is submitted.
  for pc in frame-critical frame-high frame-medium frame-low; do
    wait_for "PriorityClass $pc" k get priorityclass "$pc"
  done
  k apply -f hack/demo/jobs.yaml
  k apply -f hack/demo/storage-alerts.yaml
  wait_for "job demo-nightly-report Completed" job_phase demo-nightly-report Completed
  wait_for "job demo-training-run Running" job_phase demo-training-run Running
  wait_for "job demo-flaky-etl Failed" job_phase demo-flaky-etl Failed
  wait_for "both demo volumes Bound" volumes_bound
}

# wait_for DESCRIPTION CMD...: retry CMD for up to two minutes.
wait_for() {
  local what="$1"
  shift
  for _ in $(seq 1 60); do
    "$@" >/dev/null 2>&1 && return
    sleep 2
  done
  echo "demo: timed out waiting for ${what}:" >&2
  k -n frame-system get framejobs >&2 || true
  k -n demo-workloads get pvc,pods >&2 || true
  exit 1
}

job_phase() {
  [ "$(k -n frame-system get framejob "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}')" = "$2" ]
}

volumes_bound() {
  [ "$(k -n demo-workloads get pvc -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' | grep -c '^Bound$')" = "2" ]
}

# Single replica and the demo image tags, on a copy: the manifests in
# deploy/kubernetes name placeholder images and several replicas (with
# anti-affinity), more than a demo needs.
render() {
  sed -e "s#image: cluster-control:latest#image: ${IMG_UI}#" \
    -e "s#image: frame-uiproxy:latest#image: ${IMG_UIPROXY}#" \
    -e "s#image: cluster-control-auth:latest#image: ${IMG_AUTHD}#" \
    -e 's#value: REPLACE_HOSTNAME#value: localhost#' \
    -e 's#value: https://REPLACE_HOSTNAME#value: http://localhost:'"${PORT}"'#' \
    -e 's#^  replicas: [0-9]*#  replicas: 1#' "$1"
}

install_console() {
  log "Installing authd and the console"
  k apply -f deploy/kubernetes/base/namespace.yaml
  k apply -f deploy/kubernetes/base/rbac.yaml
  k apply -f deploy/kubernetes/base/rbac-tier-bindings.yaml
  k apply -f deploy/kubernetes/base/service.yaml
  # The console looks for Frame resources in `frameNamespace` (default:
  # `default`), which is where the demo does not put them. Its config ConfigMap
  # (src/lib/frame-config.ts) says where they are: here, `frame-system`.
  k -n cluster-control create configmap cluster-control-config \
    --from-literal=config.json='{"frameNamespace":"frame-system"}' --dry-run=client -o yaml | k apply -f -

  # authd reads the one-shot bootstrap token and its keys at start. Generate
  # them once: a rerun against a kept cluster must keep what authd already loaded.
  fresh=0
  [ -s "$STATE_DIR/bootstrap-token" ] && [ -s "$STATE_DIR/signing.pem" ] && [ -s "$STATE_DIR/challenge-hmac" ] || fresh=1
  if [ "$fresh" = 1 ]; then
    openssl rand -hex 24 >"$STATE_DIR/bootstrap-token"
    # authd's signing key (ES256) and challenge HMAC key; the console trusts
    # the JWKS authd serves.
    openssl ecparam -genkey -name prime256v1 -noout -out "$STATE_DIR/signing.pem"
    openssl rand -hex 32 >"$STATE_DIR/challenge-hmac"
  fi
  k -n cluster-control create secret generic frame-auth-keys \
    --from-file=signing.pem="$STATE_DIR/signing.pem" \
    --from-file=challenge-hmac="$STATE_DIR/challenge-hmac" --dry-run=client -o yaml | k apply -f -
  # The bootstrap Secret is deleted by authd once the first admin exists: only
  # put it back while there is none, or it would outlive its purpose.
  if [ -z "$(k get frameusers -A -o name 2>/dev/null)" ]; then
    k -n cluster-control create secret generic frame-auth-bootstrap \
      --from-file=token="$STATE_DIR/bootstrap-token" --dry-run=client -o yaml | k apply -f -
  fi

  # authd: its kustomization (certificates, RBAC, Service, Deployment) with the
  # demo values substituted in a temporary copy.
  tmp="$(mktemp -d)"
  cp -r deploy/kubernetes/authd "$tmp/authd"
  render deploy/kubernetes/authd/deployment.yaml >"$tmp/authd/deployment.yaml"
  k apply -k "$tmp/authd"
  rm -rf "$tmp"
  k -n cluster-control wait --for=condition=Ready certificate/frame-auth-tls --timeout=180s
  # New keys or token on a kept cluster: authd only reads them at start.
  if [ "$fresh" = 1 ]; then k -n cluster-control rollout restart deployment/cluster-control-auth; fi
  k -n cluster-control rollout status deployment/cluster-control-auth --timeout=180s

  render deploy/kubernetes/base/deployment.yaml | k apply -f -
  k -n cluster-control rollout status deployment/cluster-control-ui --timeout=180s
}

# Our own port-forward: the pid file is only trusted if that process really is
# a port-forward, and the console must answer through it (not through whatever
# else holds the port).
ours() { [ -n "${1:-}" ] && ps -p "$1" -o args= 2>/dev/null | grep -q 'port-forward'; }

port_forward() {
  local pid=""
  [ -f "$STATE_DIR/port-forward.pid" ] && pid="$(cat "$STATE_DIR/port-forward.pid")"
  if ours "$pid"; then return; fi
  # kubectl itself, not the k() wrapper: $! must be the port-forward process.
  kubectl -n cluster-control port-forward --address 127.0.0.1 svc/cluster-control-ui "${PORT}:80" >"$STATE_DIR/port-forward.log" 2>&1 &
  pid=$!
  echo "$pid" >"$STATE_DIR/port-forward.pid"
  for _ in $(seq 1 30); do
    kill -0 "$pid" 2>/dev/null || break
    if grep -q 'Forwarding from' "$STATE_DIR/port-forward.log" && curl -fsS -o /dev/null "http://localhost:${PORT}/health" 2>/dev/null; then
      return
    fi
    sleep 1
  done
  echo "demo: the console did not come up on http://localhost:${PORT} (port already in use? set DEMO_PORT)" >&2
  cat "$STATE_DIR/port-forward.log" >&2 || true
  exit 1
}

# Sign in as the first admin the way a browser does, then read a Hearthrack
# resource through the console: that crosses nginx, authd, the uiproxy's
# impersonation and the apiserver.
verify() {
  port_forward
  local base="http://localhost:${PORT}" token cookie body code jwt
  token="$(cat "$STATE_DIR/bootstrap-token")"
  log "Verifying: bootstrap, token, read through the console"

  # 204 the first time; 404 once an admin exists. Either way we need a session:
  # a rerun signs in with the cookie the first run kept.
  code="$(curl -sS -o /dev/null -D "$STATE_DIR/bootstrap.headers" -w '%{http_code}' \
    -X POST "$base/auth/bootstrap" -H 'Content-Type: application/json' \
    -d "{\"token\":\"${token}\",\"email\":\"${ADMIN_EMAIL}\"}")"
  case "$code" in
    204) cookie="$( { grep -i '^set-cookie:' "$STATE_DIR/bootstrap.headers" || true; } | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')"
         printf '%s' "$cookie" >"$STATE_DIR/session-cookie" ;;
    404) [ -f "$STATE_DIR/session-cookie" ] || { echo "demo: an admin already exists and no session was kept; run 'hack/demo.sh down' then 'up'" >&2; exit 1; }
         cookie="$(cat "$STATE_DIR/session-cookie")" ;;
    *)   echo "demo: /auth/bootstrap answered HTTP $code" >&2; exit 1 ;;
  esac
  [ -n "$cookie" ] || { echo "demo: no session cookie came back from /auth/bootstrap" >&2; exit 1; }

  body="$(curl -sS -w '\n%{http_code}' -X POST "$base/auth/token" -H "Cookie: $cookie")"
  code="${body##*$'\n'}"
  body="${body%$'\n'*}"
  [ "$code" = "200" ] || { echo "demo: /auth/token answered HTTP $code (session expired? run 'hack/demo.sh down' then 'up')" >&2; exit 1; }
  jwt="$(printf '%s' "$body" | sed -E 's/.*"id_token":"([^"]+)".*/\1/')"
  [ -n "$jwt" ] && [ "$jwt" != "$body" ] || { echo "demo: /auth/token returned no id_token" >&2; exit 1; }

  code="$(curl -sS -o "$STATE_DIR/framenodes.json" -w '%{http_code}' \
    -H "Authorization: Bearer $jwt" "$base/apis/frame.plume-labs.io/v1beta1/framenodes")"
  [ "$code" = "200" ] || { echo "demo: reading FrameNodes through the console answered HTTP $code" >&2; cat "$STATE_DIR/framenodes.json" >&2; exit 1; }
  grep -q '"kind": *"FrameNodeList"' "$STATE_DIR/framenodes.json" || { echo "demo: unexpected answer from the console" >&2; exit 1; }
  [ "$(grep -o '"rack": *"rack-[ab]"' "$STATE_DIR/framenodes.json" | wc -l)" = "4" ] && [ "$(grep -o '"reason": *"Online"' "$STATE_DIR/framenodes.json" | wc -l)" = "4" ] || { echo "demo: the console does not list the four demo nodes, classified and Online" >&2; exit 1; }
  # The screens' own data path: the seeded jobs and alerts come back through
  # the console proxy too, not only through kubectl.
  for kind in framejobs:3 framealerts:3; do
    code="$(curl -sS -o "$STATE_DIR/${kind%%:*}.json" -w '%{http_code}' \
      -H "Authorization: Bearer $jwt" "$base/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/${kind%%:*}")"
    [ "$code" = "200" ] || { echo "demo: reading ${kind%%:*} through the console answered HTTP $code" >&2; exit 1; }
    [ "$(grep -o '"hearthrack.demo/seeded": *"true"' "$STATE_DIR/${kind%%:*}.json" | wc -l)" = "${kind##*:}" ] || { echo "demo: the console does not list the ${kind##*:} seeded ${kind%%:*}" >&2; exit 1; }
  done
  code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $jwt" \
    "$base/api/v1/namespaces/cluster-control/configmaps/cluster-control-config")"
  [ "$code" = "200" ] || { echo "demo: the console cannot read its config ConfigMap (HTTP $code)" >&2; exit 1; }
  echo "OK: signed in as ${ADMIN_EMAIL}; read the four demo FrameNodes, three jobs and three alerts through the console."
}

up() {
  for t in docker "$KIND" kubectl go curl openssl make; do need "$t"; done
  init_state
  build_images
  create_cluster
  install_cert_manager
  install_operator
  seed
  seed_workloads
  install_console
  port_forward
  local shown
  shown="$(cat "$STATE_DIR/bootstrap-token")"
  # Never in a CI log: say where it is instead.
  [ -z "${CI:-}" ] || shown="<the content of $STATE_DIR/bootstrap-token>"
  cat <<EOF

Hearthrack is running at http://localhost:${PORT} (the port-forward is started by
this script and stopped by 'hack/demo.sh down').

The first admin has no password, only a passkey. From the login page, open the
browser devtools console and run:

  await fetch('/auth/bootstrap', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({token:'${shown}', email:'${ADMIN_EMAIL}'})})

Reload, then enrol a passkey from the "Passkeys" entry in the sidebar footer,
right away: the session lasts 12 hours, and an admin with no passkey enrolled
in that window cannot sign in again (then run 'hack/demo.sh down' and 'up').
(The token is single-use, and 'hack/demo.sh verify' uses it: run one or the
other on a given cluster.) The Nodes screens show the four demo nodes, in two
racks; jobs, volumes and alerts are not seeded yet.
EOF
}

down() {
  local pid=""
  [ -f "$STATE_DIR/port-forward.pid" ] && pid="$(cat "$STATE_DIR/port-forward.pid")"
  if ours "$pid"; then kill "$pid" 2>/dev/null || true; fi
  "$KIND" delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" 2>/dev/null || "$KIND" delete cluster --name "$CLUSTER"
  # Only a directory this script created (marker), never a path taken on trust.
  if [ -f "$MARKER" ]; then rm -rf "$STATE_DIR"; fi
}

case "${1:-}" in
  up) up ;;
  verify) need curl; need kubectl; [ -f "$MARKER" ] || { echo "demo: no demo state in $STATE_DIR: run 'hack/demo.sh up' first" >&2; exit 1; }; verify ;;
  down) need "$KIND"; down ;;
  *) echo "usage: $0 up|verify|down" >&2; exit 2 ;;
esac
