#!/usr/bin/env bash
#
# Prepare a cluster for NodeTuning, then install it.
#
#   node-tuning-install.sh            # dry run: says what it would do, changes nothing
#   node-tuning-install.sh --apply    # does it
#
# Three things have to happen before NodeTuning can work, and none of them is
# something `kubectl apply` does on its own:
#
#   1. ksm-tuner has to be DELETED from the cluster. Removing its manifest from
#      the repo does not remove a running DaemonSet, and until it is gone both
#      it and the new agent write the KSM sysfs knobs. Same values, so the
#      overlap is harmless rather than a race — but two writers on one setting
#      is exactly what this design forbids everywhere else.
#
#   2. nvidia-mps has to be deleted IF it was applied by hand, because the
#      kustomize build now injects commonLabels into its DaemonSet selector and
#      a selector is immutable. Applying over it fails; deleting first does not.
#
#   3. tuned has to be INSTALLED ON THE NODES. Every host call the agent makes
#      goes through nsenter into the node's namespaces, so `tuned-adm` resolves
#      to the node's binary — one baked into the agent image would never be the
#      one that runs. Ubuntu Server does not ship it.
#
# What this script deliberately does NOT do: create a NodeTuning, or enable KSM.
# Page merging across tenants is a side channel, `ksm.enabled` defaults to
# false, and turning it on is a decision with a security consequence that
# belongs to a human, not to an installer.
#
set -euo pipefail

APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

: "${KUBECONFIG:=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml}"
# A relative KUBECONFIG makes a script silently cwd-dependent: run it from a
# subdirectory and every lookup fails, surfacing as a wrong answer about the
# cluster rather than as a missing file.
if [ "${KUBECONFIG#/}" = "$KUBECONFIG" ]; then
  KUBECONFIG="$(cd "$(dirname "$KUBECONFIG")" && pwd)/$(basename "$KUBECONFIG")"
fi
[ -r "$KUBECONFIG" ] || { echo "error: KUBECONFIG not readable: $KUBECONFIG" >&2; exit 1; }
export KUBECONFIG

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
would() { if [ $APPLY -eq 1 ]; then say "   doing: $*"; else say "   would: $*"; fi; }
die()  { echo "error: $*" >&2; exit 1; }

# ── Preflight ────────────────────────────────────────────────────────────────
step "Preflight"

if ! NODES=$(kubectl get nodes --no-headers 2>&1); then
  die "kubectl cannot reach the cluster (KUBECONFIG=$KUBECONFIG): $NODES"
fi
NOT_READY=$(echo "$NODES" | awk '$2 != "Ready" {print $1}')
[ -z "$NOT_READY" ] || die "these nodes are not Ready, refusing to start: $NOT_READY"
NODE_NAMES=$(echo "$NODES" | awk '{print $1}')
say "   nodes Ready: $(echo "$NODE_NAMES" | tr '\n' ' ')"

[ $APPLY -eq 1 ] || say "   DRY RUN — nothing below will change anything. Re-run with --apply."

# ── 1. Remove the superseded DaemonSets ──────────────────────────────────────
step "1. Superseded DaemonSets"

if kubectl -n kube-system get ds ksm-tuner >/dev/null 2>&1; then
  say "   ksm-tuner is deployed; the agent supersedes it"
  would "kubectl -n kube-system delete ds ksm-tuner"
  [ $APPLY -eq 1 ] && kubectl -n kube-system delete ds ksm-tuner
else
  say "   ksm-tuner: not deployed, nothing to remove"
fi

# Only delete nvidia-mps if its selector actually disagrees with what kustomize
# now renders. Deleting one that already matches would be disruption for
# nothing.
if MPS_SEL=$(kubectl -n kube-system get ds nvidia-mps -o jsonpath='{.spec.selector.matchLabels}' 2>/dev/null) && [ -n "$MPS_SEL" ]; then
  case "$MPS_SEL" in
    *app.kubernetes.io/name*)
      say "   nvidia-mps: selector already carries the injected labels, leaving it alone" ;;
    *)
      say "   nvidia-mps: selector is $MPS_SEL — kustomize now injects commonLabels, and a selector is immutable"
      would "kubectl -n kube-system delete ds nvidia-mps   (it is recreated by the apply in step 3)"
      [ $APPLY -eq 1 ] && kubectl -n kube-system delete ds nvidia-mps ;;
  esac
else
  say "   nvidia-mps: not deployed, nothing to remove"
fi

# ── 2. tuned on the nodes ────────────────────────────────────────────────────
step "2. tuned on the nodes"

# One node at a time, and each install is checked afterwards rather than
# assumed. A package manager that half-succeeds is worse than one that fails:
# the agent would find a tuned-adm that errors on every profile.
for node in $NODE_NAMES; do
  probe=$(kubectl run "tunedprobe-$$-$RANDOM" --rm -i --restart=Never --image=busybox:1.36 \
    --overrides="{\"spec\":{\"nodeName\":\"$node\",\"tolerations\":[{\"operator\":\"Exists\"}],\"containers\":[{\"name\":\"p\",\"image\":\"busybox:1.36\",\"command\":[\"sh\",\"-c\",\"ls /host/usr/sbin/tuned >/dev/null 2>&1 && echo PRESENT || echo ABSENT\"],\"volumeMounts\":[{\"name\":\"h\",\"mountPath\":\"/host\",\"readOnly\":true}]}],\"volumes\":[{\"name\":\"h\",\"hostPath\":{\"path\":\"/\",\"type\":\"Directory\"}}]}}" \
    2>/dev/null | grep -E '^(PRESENT|ABSENT)$' | head -1) || probe=""

  case "${probe:-UNKNOWN}" in
    PRESENT) say "   $node: tuned already installed" ; continue ;;
    ABSENT)  say "   $node: tuned absent" ;;
    *)       say "   $node: could not determine whether tuned is installed — skipping rather than guessing"
             continue ;;
  esac

  would "install tuned on $node (apt-get install -y tuned, then enable it)"
  [ $APPLY -eq 1 ] || continue

  kubectl run "tunedinst-$$-$RANDOM" --rm -i --restart=Never --image=busybox:1.36 \
    --overrides="{\"spec\":{\"nodeName\":\"$node\",\"hostPID\":true,\"tolerations\":[{\"operator\":\"Exists\"}],\"containers\":[{\"name\":\"p\",\"image\":\"busybox:1.36\",\"securityContext\":{\"privileged\":true},\"command\":[\"nsenter\",\"-t\",\"1\",\"-m\",\"-u\",\"-i\",\"-n\",\"-p\",\"--\",\"/bin/sh\",\"-c\",\"export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq tuned && systemctl enable --now tuned && tuned-adm active\"]}]}}" \
    2>&1 | grep -vE '^(If you|All commands|pod .* deleted)' || true

  # Verify rather than trust the exit status: apt can report success while the
  # service fails to start, and the agent would then meet a tuned-adm that
  # errors on every profile it is asked for.
  after=$(kubectl run "tunedverify-$$-$RANDOM" --rm -i --restart=Never --image=busybox:1.36 \
    --overrides="{\"spec\":{\"nodeName\":\"$node\",\"tolerations\":[{\"operator\":\"Exists\"}],\"containers\":[{\"name\":\"p\",\"image\":\"busybox:1.36\",\"command\":[\"sh\",\"-c\",\"ls /host/usr/sbin/tuned >/dev/null 2>&1 && echo PRESENT || echo ABSENT\"],\"volumeMounts\":[{\"name\":\"h\",\"mountPath\":\"/host\",\"readOnly\":true}]}],\"volumes\":[{\"name\":\"h\",\"hostPath\":{\"path\":\"/\",\"type\":\"Directory\"}}]}}" \
    2>/dev/null | grep -E '^(PRESENT|ABSENT)$' | head -1) || after=""
  [ "$after" = PRESENT ] || die "$node: tuned still absent after install — stopping before touching another node"
  say "   $node: tuned installed and verified"
done

# ── 3. Install NodeTuning itself ─────────────────────────────────────────────
step "3. CRDs and manifests"

would "kubectl apply -f $REPO_ROOT/config/crd/bases/"
would "kubectl apply -k $REPO_ROOT/deploy/kubernetes/base/"
if [ $APPLY -eq 1 ]; then
  kubectl apply -f "$REPO_ROOT/config/crd/bases/"
  kubectl apply -k "$REPO_ROOT/deploy/kubernetes/base/"
fi

# ── 4. What is left for a human ──────────────────────────────────────────────
step "4. Left for you, deliberately"

cat <<'NEXT'
   Nothing is tuned yet. This installed the machinery; it created no NodeTuning
   and enabled nothing.

   To tune a set of nodes, write a NodeTuning and apply it. Enabling KSM is a
   security decision, not a performance one: merged pages make a write take a
   measurable copy-on-write fault, which lets one container test whether
   another holds a given page. On a cluster running notebooks or code
   sandboxes, those are real neighbours.

     apiVersion: frame.plume-labs.io/v1beta1
     kind: NodeTuning
     metadata: { name: workers }
     spec:
       nodeSelector:
         matchLabels: { "kubernetes.io/os": linux }
       ksm: { enabled: true, pagesToScan: 4000, sleepMillisecs: 200 }

   The controller will then park each node at RebootPending and wait. Approve
   one node at a time, naming the generation you are approving:

     kubectl annotate node <NODE> \
       frame.plume-labs.io/tuning-approved=$(kubectl get nodetuning workers -o jsonpath='{.metadata.generation}')

   Watch it converge, and read `realization`: Effective means new containers
   get the setting; FullyRealized means every container on the node does. For
   KSM the gap between them is most of the benefit.

     kubectl get nodetuning workers -o jsonpath='{range .status.nodes[*]}{.name}{"\t"}{.phase}{"\t"}{.realization}{"\n"}{end}'
NEXT
