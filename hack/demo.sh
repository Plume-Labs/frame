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
# creates: every kubectl call is pinned to its context.
#
# What this slice does NOT do yet: seed demo objects (nodes, jobs, volumes),
# so the console opens empty. See docs/known-limitations.md.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

CLUSTER="${DEMO_CLUSTER:-hearthrack-demo}"
PORT="${DEMO_PORT:-8080}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.20.2}" # keep in step with test/utils/utils.go
KIND="${KIND:-kind}"
CTX="kind-${CLUSTER}"
ADMIN_EMAIL="${DEMO_ADMIN_EMAIL:-admin@example.com}"
STATE_DIR="${DEMO_STATE_DIR:-${TMPDIR:-/tmp}/hearthrack-demo}"

IMG_CONTROLLER="frame-controller:demo"
IMG_UI="cluster-control:demo"
IMG_UIPROXY="frame-uiproxy:demo"
IMG_AUTHD="cluster-control-auth:demo"

k() { kubectl --context "$CTX" "$@"; }
log() { printf '\n==> %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "demo: '$1' is required but not installed" >&2; exit 1; }; }

build_images() {
  log "Building images"
  docker build -q -f Dockerfile.controller -t "$IMG_CONTROLLER" .
  docker build -q -t "$IMG_UI" .
  docker build -q -f Dockerfile.uiproxy -t "$IMG_UIPROXY" .
  docker build -q -f Dockerfile.authd -t "$IMG_AUTHD" .
}

create_cluster() {
  if "$KIND" get clusters | grep -qx "$CLUSTER"; then
    log "Kind cluster '$CLUSTER' already exists, reusing it"
  else
    log "Creating Kind cluster '$CLUSTER'"
    "$KIND" create cluster --name "$CLUSTER"
  fi
  "$KIND" load docker-image --name "$CLUSTER" "$IMG_CONTROLLER" "$IMG_UI" "$IMG_UIPROXY" "$IMG_AUTHD"
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
  # make deploy uses the current context, which kind sets to this cluster.
  kubectl config use-context "$CTX" >/dev/null
  make deploy DEPLOY_DEFAULT=1 IMG="$IMG_CONTROLLER"
  k -n frame-system rollout status deployment/frame-controller-manager --timeout=180s
}

# Single replica and the demo image tags, on a copy: the manifests in
# deploy/kubernetes name placeholder images and several replicas, which a
# one-node cluster cannot schedule.
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

  # authd reads the one-shot bootstrap token at start: the Secret comes first.
  mkdir -p "$STATE_DIR"
  umask 077
  openssl rand -hex 24 >"$STATE_DIR/bootstrap-token"
  k -n cluster-control create secret generic frame-auth-bootstrap \
    --from-file=token="$STATE_DIR/bootstrap-token" --dry-run=client -o yaml | k apply -f -

  # authd: its kustomization (certificates, RBAC, Service, Deployment) with the
  # demo values substituted in a temporary copy.
  tmp="$(mktemp -d)"
  cp -r deploy/kubernetes/authd "$tmp/authd"
  render deploy/kubernetes/authd/deployment.yaml >"$tmp/authd/deployment.yaml"
  k apply -k "$tmp/authd"
  rm -rf "$tmp"
  k -n cluster-control wait --for=condition=Ready certificate/frame-auth-tls --timeout=180s
  k -n cluster-control rollout status deployment/cluster-control-auth --timeout=180s

  render deploy/kubernetes/base/deployment.yaml | k apply -f -
  k -n cluster-control rollout status deployment/cluster-control-ui --timeout=180s
}

port_forward() {
  mkdir -p "$STATE_DIR"
  if [ -f "$STATE_DIR/port-forward.pid" ] && kill -0 "$(cat "$STATE_DIR/port-forward.pid")" 2>/dev/null; then
    return
  fi
  k -n cluster-control port-forward svc/cluster-control-ui "${PORT}:80" >"$STATE_DIR/port-forward.log" 2>&1 &
  echo $! >"$STATE_DIR/port-forward.pid"
  for _ in $(seq 1 30); do
    curl -fsS -o /dev/null "http://localhost:${PORT}/health" 2>/dev/null && return
    sleep 1
  done
  echo "demo: the console did not answer on http://localhost:${PORT}" >&2
  exit 1
}

# Sign in as the first admin the way a browser does, then read a Hearthrack
# resource through the console: that crosses nginx, authd, the uiproxy's
# impersonation and the apiserver.
verify() {
  port_forward
  local base="http://localhost:${PORT}" token cookie body code
  token="$(cat "$STATE_DIR/bootstrap-token")"
  log "Verifying: bootstrap, token, read through the console"

  # 204 the first time; 404 once an admin exists. Either way we need a session:
  # a rerun signs in with the cookie the first run kept.
  code="$(curl -sS -o /dev/null -D "$STATE_DIR/bootstrap.headers" -w '%{http_code}' \
    -X POST "$base/auth/bootstrap" -H 'Content-Type: application/json' \
    -d "{\"token\":\"${token}\",\"email\":\"${ADMIN_EMAIL}\"}")"
  case "$code" in
    204) cookie="$(grep -i '^set-cookie:' "$STATE_DIR/bootstrap.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')"
         printf '%s' "$cookie" >"$STATE_DIR/session-cookie" ;;
    404) [ -f "$STATE_DIR/session-cookie" ] || { echo "demo: an admin already exists and no session was kept; run 'hack/demo.sh down' then 'up'" >&2; exit 1; }
         cookie="$(cat "$STATE_DIR/session-cookie")" ;;
    *)   echo "demo: /auth/bootstrap answered HTTP $code" >&2; exit 1 ;;
  esac
  [ -n "$cookie" ] || { echo "demo: no session cookie came back from /auth/bootstrap" >&2; exit 1; }

  body="$(curl -fsS -X POST "$base/auth/token" -H "Cookie: $cookie")"
  jwt="$(printf '%s' "$body" | sed -E 's/.*"id_token":"([^"]+)".*/\1/')"
  [ -n "$jwt" ] && [ "$jwt" != "$body" ] || { echo "demo: /auth/token returned no id_token" >&2; exit 1; }

  code="$(curl -sS -o "$STATE_DIR/framenodes.json" -w '%{http_code}' \
    -H "Authorization: Bearer $jwt" "$base/apis/frame.plume-labs.io/v1beta1/framenodes")"
  [ "$code" = "200" ] || { echo "demo: reading FrameNodes through the console answered HTTP $code" >&2; cat "$STATE_DIR/framenodes.json" >&2; exit 1; }
  grep -q '"kind": *"FrameNodeList"' "$STATE_DIR/framenodes.json" || { echo "demo: unexpected answer from the console" >&2; exit 1; }
  echo "OK: signed in as ${ADMIN_EMAIL} and read FrameNodes through the console."
}

up() {
  for t in docker "$KIND" kubectl go curl openssl make; do need "$t"; done
  build_images
  create_cluster
  install_cert_manager
  install_operator
  install_console
  port_forward
  cat <<EOF

Hearthrack is running at http://localhost:${PORT} (the port-forward is started by
this script and stopped by 'hack/demo.sh down').

The first admin has no password, only a passkey. From the login page, open the
browser devtools console and run:

  await fetch('/auth/bootstrap', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({token:'$(cat "$STATE_DIR/bootstrap-token")', email:'${ADMIN_EMAIL}'})})

Reload, then enrol a passkey from the "Passkeys" entry in the sidebar footer.
(The token is single-use, and 'hack/demo.sh verify' uses it: run one or the
other on a given cluster.) The console is empty for now: no demo objects are
seeded yet.
EOF
}

down() {
  if [ -f "$STATE_DIR/port-forward.pid" ]; then
    kill "$(cat "$STATE_DIR/port-forward.pid")" 2>/dev/null || true
  fi
  rm -rf "$STATE_DIR"
  "$KIND" delete cluster --name "$CLUSTER"
}

case "${1:-}" in
  up) up ;;
  verify) need curl; verify ;;
  down) need "$KIND"; down ;;
  *) echo "usage: $0 up|verify|down" >&2; exit 2 ;;
esac
