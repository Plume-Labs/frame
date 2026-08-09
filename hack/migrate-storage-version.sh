#!/usr/bin/env bash
# Rewrite every stored Frame CR at the current storage version, then record on
# each CRD that nothing is stored at the old version any more.
#
# Why this is a script and not a one-liner:
#
#   1. `.status.storedVersions` only ever grows, and nothing prunes it. The
#      apiserver appends the new storage version in the CRD registry's
#      PrepareForCreate/PrepareForUpdate — i.e. the moment an apply makes a
#      version the storage version, whether or not a single object is ever
#      written at it — and there is no code path anywhere in
#      apiextensions-apiserver that removes an entry. Not when the last object
#      at the old version is rewritten, not ever. A version cannot be dropped
#      from `spec.versions` while it appears in `storedVersions`, so the list
#      has to be pruned by hand.
#
#      That final patch is therefore a *claim*: it asserts no object remains
#      stored at the old version. If the rewrite below missed one, the claim
#      is a lie and removing the version later makes that object unreadable.
#      Hence: rewrite everything first, fail loudly on any error, and only
#      then patch.
#
#   2. Objects are rewritten at the storage version on any write, so a no-op
#      annotation patch is enough. A kind with *zero* objects has nothing to
#      rewrite — four of Frame's eight are in exactly that state on the live
#      cluster (FrameUser, TalosMachineConfig, TalosUpgrade, FrameService) —
#      and for those the status patch is the whole migration.
#
#   3. Rewriting a FrameJob or a FrameNode re-triggers its controller. That is
#      wanted — the legacy stored `status.phase` does not survive conversion
#      and only a reconcile puts a real Ready condition in its place — but for
#      FrameJob it is not free: if a job's Argo Workflow has been garbage
#      collected, the controller's IsNotFound branch creates a new one and
#      silently re-runs a completed job. Check the Workflows exist before
#      running this against a cluster with completed jobs on it:
#
#        kubectl get framejobs -A \
#          -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,WF:.status.argoWorkflowName
#        kubectl get workflows.argoproj.io -A
#
# Usage: KUBECONFIG=... ./hack/migrate-storage-version.sh [--apply]
# Without --apply it reports what it would do and changes nothing.
set -euo pipefail

STORAGE_VERSION="v1beta1"

APPLY=0
case "${1:-}" in
  --apply) APPLY=1 ;;
  "") ;;
  *) echo "usage: $0 [--apply]" >&2; exit 2 ;;
esac

CRDS=(
  framejobs.frame.plume-labs.io
  framenodes.frame.plume-labs.io
  frameresourcequotas.frame.plume-labs.io
  schedulingpolicies.frame.plume-labs.io
  talosmachineconfigs.frame.plume-labs.io
  talosupgrades.frame.plume-labs.io
  frameusers.frame.plume-labs.io
  frameservices.services.plume-labs.io
)

expected="[\"$STORAGE_VERSION\"]"
migrated=0
skipped=0

for crd in "${CRDS[@]}"; do
  stored="$(kubectl get crd "$crd" -o jsonpath='{.status.storedVersions}')"
  echo "== $crd  storedVersions=$stored"

  # Refuse to touch a CRD whose storage version is not the one we are
  # migrating to: patching storedVersions to a version the apiserver is not
  # writing would claim a migration that never happened.
  actual_storage="$(kubectl get crd "$crd" \
    -o jsonpath='{.spec.versions[?(@.storage==true)].name}')"
  if [ "$actual_storage" != "$STORAGE_VERSION" ]; then
    echo "   FAILED: storage version is '$actual_storage', expected '$STORAGE_VERSION'." >&2
    echo "   Apply the current CRDs (and the manager that serves /convert) first." >&2
    exit 1
  fi

  if [ "$stored" = "$expected" ]; then
    echo "   already migrated"
    skipped=$((skipped + 1))
    continue
  fi

  # Capture the listing into a variable rather than piping it into mapfile:
  # inside a process substitution a kubectl failure is invisible, and an empty
  # result would be read as "no objects" and answered with a status patch —
  # which is the one mistake here that loses data.
  if ! refs="$(kubectl get "$crd" -A \
      -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\n"}{end}')"; then
    echo "   FAILED to list $crd — refusing to declare it migrated" >&2
    exit 1
  fi

  count=0
  while IFS=$'\t' read -r ns name; do
    [ -n "$name" ] || continue
    count=$((count + 1))
    echo "   rewrite $ns/$name"
    if [ "$APPLY" -eq 1 ]; then
      kubectl patch "$crd" "$name" -n "$ns" --type=merge \
        -p "{\"metadata\":{\"annotations\":{\"frame.plume-labs.io/storage-migrated\":\"$STORAGE_VERSION\"}}}" \
        >/dev/null
    fi
  done <<<"$refs"

  if [ "$count" -eq 0 ]; then
    echo "   no stored objects — nothing to rewrite, only the CRD status to correct"
  fi

  if [ "$APPLY" -eq 1 ]; then
    # Every object is now stored at $STORAGE_VERSION. The apiserver will not
    # prune the old entry itself, so say so explicitly.
    kubectl patch crd "$crd" --subresource=status --type=merge \
      -p "{\"status\":{\"storedVersions\":[\"$STORAGE_VERSION\"]}}" >/dev/null
    stored="$(kubectl get crd "$crd" -o jsonpath='{.status.storedVersions}')"
    echo "   storedVersions=$stored"
    if [ "$stored" != "$expected" ]; then
      echo "   FAILED to converge — do not remove $crd's old version" >&2
      exit 1
    fi
    migrated=$((migrated + 1))
  fi
done

echo
if [ "$APPLY" -eq 1 ]; then
  echo "Migration complete: $migrated CRD(s) migrated, $skipped already at $STORAGE_VERSION."
  echo "Every CRD now stores only $STORAGE_VERSION."
else
  echo "Dry run. Re-run with --apply to perform the migration."
fi
