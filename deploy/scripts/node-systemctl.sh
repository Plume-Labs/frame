#!/usr/bin/env bash
#
# Restart a systemd unit on one cluster node, from inside the cluster.
#
# There is no SSH access to these nodes, so the only route to their PID 1 is a
# privileged pod that nsenters into it. That is host-root-equivalent, which is
# why this script exists instead of a blanket permission: the operation is
# parameterised, both parameters are validated against allowlists, and it
# refuses to work on more than one node at a time.
#
#   node-systemctl.sh restart <node> <unit>
#   node-systemctl.sh status  <node> <unit>
#
# The restart is handed to a transient systemd timer rather than run inline.
# Restarting k3s stops the kubelet that owns the very pod issuing the command,
# so an inline `systemctl restart` kills its own caller partway through and the
# unit may never come back. systemd-run detaches it from this pod's lifetime.
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
: "${KUBECONFIG:=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml}"
# A relative KUBECONFIG — which is what the project settings export — makes this
# script silently cwd-dependent: run it from a subdirectory and kubectl finds no
# config, every lookup fails, and the failure surfaces as a wrong answer about
# the cluster rather than as a missing file. Resolve it once, up front.
if [ "${KUBECONFIG#/}" = "$KUBECONFIG" ]; then
  KUBECONFIG="$(cd "$(dirname "$KUBECONFIG")" 2>/dev/null && pwd)/$(basename "$KUBECONFIG")" \
    || { echo "error: relative KUBECONFIG '$KUBECONFIG' does not resolve from $(pwd)" >&2; exit 1; }
fi
[ -r "$KUBECONFIG" ] || { echo "error: KUBECONFIG not readable: $KUBECONFIG" >&2; exit 1; }
export KUBECONFIG

# Only units whose restart is a known, recoverable node operation. k3s and
# k3s-agent bounce the kubelet and containerd; that is disruptive but designed
# for. Anything else — sshd, systemd-networkd, the firewall — can strand a node
# with no way back in, and this script has no console to recover from.
ALLOWED_UNITS="k3s k3s-agent containerd kubelet"

READY_TIMEOUT=${READY_TIMEOUT:-240}

die() { echo "error: $*" >&2; exit 1; }

usage() {
  cat >&2 <<USAGE
usage: node-systemctl.sh <restart|status> <node> <unit>

  node   must be a node this cluster currently reports
  unit   one of: $ALLOWED_UNITS

env:
  KUBECONFIG     defaults to the Frame test cluster
  READY_TIMEOUT  seconds to wait for the node to return Ready (default 240)
USAGE
  exit 2
}

[ $# -eq 3 ] || usage
ACTION=$1; NODE=$2; UNIT=$3

case "$ACTION" in
  restart|status) ;;
  *) usage ;;
esac

# Validate the unit against the allowlist rather than passing it through. It is
# interpolated into a shell command inside the node's PID 1 namespace, so an
# unchecked value here is arbitrary root execution with extra steps.
case " $ALLOWED_UNITS " in
  *" $UNIT "*) ;;
  *) die "unit '$UNIT' is not in the allowlist: $ALLOWED_UNITS" ;;
esac

# Validate the node against what the cluster actually reports, for the same
# reason, and so a typo cannot silently target nothing.
# Distinguish "the cluster says no such node" from "kubectl could not answer".
# Reporting the second as the first is what sent the last caller looking for a
# renamed node when the real fault was an unreachable kubeconfig.
if ! NODE_LIST=$(kubectl get nodes -o name 2>&1); then
  die "kubectl could not reach the cluster (KUBECONFIG=$KUBECONFIG): $NODE_LIST"
fi
case "$NODE_LIST" in
  *"node/$NODE"*) ;;
  *) die "no such node: $NODE (cluster reports: $(echo "$NODE_LIST" | tr '\n' ' '))" ;;
esac

# Refuse to disrupt a second node while another is already down. Losing one
# node is a rolling operation; losing two at once on a three-node cluster is an
# outage, and that is exactly the mistake a script run in a loop would make.
NOT_READY=$(kubectl get nodes --no-headers \
  | awk -v skip="$NODE" '$1 != skip && $2 != "Ready" {print $1}')
if [ -n "$NOT_READY" ]; then
  die "these nodes are not Ready, refusing to touch another: $NOT_READY"
fi

# $$ and $RANDOM rather than `tr </dev/urandom | head -c 6`: head closes the
# pipe as soon as it has its bytes, tr dies of SIGPIPE, and under `set -o
# pipefail` the whole script exits 141 right here — silently, before doing
# anything, which reads exactly like a successful no-op.
POD="node-systemctl-$$-$RANDOM"

run_on_node() {
  local script=$1
  kubectl run "$POD" --rm -i --restart=Never --image=busybox:1.36 \
    --overrides="$(cat <<JSON
{"spec":{
  "nodeName":"$NODE",
  "hostPID":true,
  "tolerations":[{"operator":"Exists"}],
  "containers":[{
    "name":"p","image":"busybox:1.36",
    "securityContext":{"privileged":true},
    "command":["nsenter","-t","1","-m","-u","-i","-n","-p","--","/bin/sh","-c","$script"]
  }]
}}
JSON
)" 2>&1 | { grep -vE '^(If you|All commands|pod ".*" deleted)' || true; }
  # `grep -v` exits 1 when it filters everything out, and under `set -o pipefail`
  # that aborts the script mid-operation with no message — which would have hidden
  # a restart that never fired behind a silent, successful-looking exit.
}

if [ "$ACTION" = status ]; then
  # ActiveEnterTimestamp is the honest restart signal. Node readiness is not:
  # k3s-agent comes back in a few seconds, while a node only reports NotReady
  # after missing its lease for ~40s, so a successful restart routinely leaves
  # the node Ready throughout. MemoryKSM shows whether the drop-in is live in
  # the running unit, as opposed to merely present on disk.
  run_on_node "systemctl is-active $UNIT; systemctl show $UNIT -p ActiveEnterTimestamp -p DropInPaths -p MemoryKSM"
  exit 0
fi

echo "restarting $UNIT on $NODE (timeout ${READY_TIMEOUT}s)"
echo "  other nodes verified Ready first"

# Record when the unit last started. This, not node readiness, is what proves a
# restart happened: k3s-agent is back in seconds, while a node only reports
# NotReady after ~40s of missed lease, so a perfectly good restart usually
# leaves the node Ready the whole way through. Waiting for it to flap reports
# failure on success.
BEFORE=$(run_on_node "systemctl show $UNIT -p ActiveEnterTimestamp --value" | tail -1)
echo "  unit started at: ${BEFORE:-unknown}"

# --on-active gives the pod time to exit cleanly before the kubelet goes away.
run_on_node "systemd-run --on-active=8 --unit=node-systemctl-apply --description='restart $UNIT via node-systemctl.sh' /bin/sh -c 'systemctl daemon-reload && systemctl restart $UNIT' && echo scheduled"

echo "  waiting for the unit to report a new start time"
deadline=$(( SECONDS + READY_TIMEOUT ))
while [ $SECONDS -lt $deadline ]; do
  sleep 15
  # Every probe here must tolerate failure. Restarting k3s on the server node
  # takes the apiserver down for a few seconds, so kubectl is *expected* to fail
  # mid-wait — and under `set -e` a single non-zero status ends the script right
  # where it is most important that it keep reporting. That is exactly what
  # happened on the first control-plane restart: the unit came back fine, the
  # script had already exited without a word.
  after=$(run_on_node "systemctl show $UNIT -p ActiveEnterTimestamp --value" 2>/dev/null | tail -1) || after=""
  state=$(kubectl get node "$NODE" --no-headers 2>/dev/null | awk '{print $2}') || state=""
  echo "    ${SECONDS}s: node=${state:-unknown} started=${after:-unknown}"
  if [ -n "$after" ] && [ "$after" != "$BEFORE" ]; then
    # Restarted. Still require the node back in service before returning, so a
    # caller looping over nodes never moves on to the next one too early.
    [ "$state" = Ready ] || continue
    echo "  $UNIT restarted and $NODE is Ready"
    echo "  note: only containers created from now on inherit the unit's new"
    echo "        settings; existing pods keep what they started with."
    exit 0
  fi
done

die "$UNIT on $NODE still reports start time '$BEFORE' after ${READY_TIMEOUT}s — the restart did not fire"
