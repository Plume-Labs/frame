#!/usr/bin/env bash
# Crée le compte « opérateur » du tableau de bord Ceph et le Secret
# frame-system/frame-ceph-operator dont dépend l'assistant « Remplacer un OSD »
# (docs/osd-replacement.md, « The operator account »).
#
# À lancer par une personne, depuis une machine qui a le kubeconfig du cluster
# (pas par un agent : c'est une écriture dans Ceph et dans le cluster).
#
#   hack/ceph-operator-account.sh            crée le compte et le Secret s'ils manquent ; s'arrête si le Secret existe
#   hack/ceph-operator-account.sh --rotate   change aussi le mot de passe et réécrit le Secret
#
# Ce que le script écrit, et rien d'autre :
#   - Ceph : le rôle frame-osd-operator (osd: read, update, create ; pool: read)
#            et le compte du même nom, mot de passe tiré au hasard ;
#   - Kubernetes : le Secret frame-ceph-operator dans frame-system.
# Le mot de passe vit dans un fichier temporaire 0600, effacé à la sortie ; il
# n'est jamais affiché. Le CA et le nom de serveur viennent du Secret du compte
# de lecture (frame-ceph-dashboard), déjà en place pour l'écran Stockage.
#
# Variables : CEPH_NAMESPACE (rook-ceph), FRAME_NAMESPACE (frame-system),
# DASHBOARD_SERVICE (rook-ceph-mgr-dashboard), DASHBOARD_PORT (8443).
set -euo pipefail

CEPH_NS="${CEPH_NAMESPACE:-rook-ceph}"
FRAME_NS="${FRAME_NAMESPACE:-frame-system}"
SVC_NAME="${DASHBOARD_SERVICE:-rook-ceph-mgr-dashboard}"
PORT="${DASHBOARD_PORT:-8443}"
ACCOUNT=frame-osd-operator
SECRET=frame-ceph-operator
READ_SECRET=frame-ceph-dashboard

ROTATE=0
case "${1:-}" in
  "") ;;
  --rotate) ROTATE=1 ;;
  *) echo "usage: $0 [--rotate]" >&2; exit 2 ;;
esac

die() { echo "⛔ $*" >&2; exit 1; }
step() { echo; echo "== $* =="; }

command -v kubectl >/dev/null || die "kubectl introuvable"
command -v openssl >/dev/null || die "openssl introuvable"
echo "Cluster : $(kubectl config current-context)  (Ctrl-C maintenant si ce n'est pas le bon)"

TOOLS=(kubectl -n "$CEPH_NS" exec deploy/rook-ceph-tools --)
"${TOOLS[@]}" ceph dashboard get-jwt-token-ttl >/dev/null 2>&1 \
  || die "le toolbox de Ceph ne répond pas (deploy/rook-ceph-tools dans $CEPH_NS) ou le tableau de bord n'est pas activé"

SECRET_EXISTS=0
if OUT=$(kubectl -n "$FRAME_NS" get secret "$SECRET" -o name 2>&1); then
  SECRET_EXISTS=1
elif ! grep -q NotFound <<<"$OUT"; then
  die "lecture du Secret $FRAME_NS/$SECRET impossible : $OUT"
fi
if [ "$SECRET_EXISTS" = 1 ] && [ "$ROTATE" = 0 ]; then
  echo "Le Secret $FRAME_NS/$SECRET existe déjà : rien à faire."
  echo "(Il ne marche pas ? --rotate refait le mot de passe ; voir aussi docs/storage-mgr-account.md pour un login refusé.)"
  exit 0
fi

kubectl -n "$FRAME_NS" get secret "$READ_SECRET" >/dev/null 2>&1 \
  || die "le Secret $FRAME_NS/$READ_SECRET (compte de lecture de l'écran Stockage) manque : le CA du tableau de bord en vient. Voir docs/storage-mgr-account.md"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; "${TOOLS[@]}" rm -f /tmp/frame-osd.pass >/dev/null 2>&1 || true' EXIT
umask 077

step "1. Rôle $ACCOUNT"
if "${TOOLS[@]}" ceph dashboard ac-role-show "$ACCOUNT" >/dev/null 2>&1; then
  echo "le rôle existe"
else
  "${TOOLS[@]}" ceph dashboard ac-role-create "$ACCOUNT" "Frame manager: OSD replacement"
fi
"${TOOLS[@]}" ceph dashboard ac-role-add-scope-perms "$ACCOUNT" osd read update create
"${TOOLS[@]}" ceph dashboard ac-role-add-scope-perms "$ACCOUNT" pool read
"${TOOLS[@]}" ceph dashboard ac-role-show "$ACCOUNT"

step "2. Compte $ACCOUNT"
openssl rand -base64 32 | tr -d '\n' >"$WORK/pass"
kubectl -n "$CEPH_NS" exec -i deploy/rook-ceph-tools -- sh -c 'umask 077; cat >/tmp/frame-osd.pass' <"$WORK/pass"
if "${TOOLS[@]}" ceph dashboard ac-user-show "$ACCOUNT" >/dev/null 2>&1; then
  echo "le compte existe : nouveau mot de passe"
  "${TOOLS[@]}" ceph dashboard ac-user-set-password "$ACCOUNT" -i /tmp/frame-osd.pass --force-password >/dev/null
  "${TOOLS[@]}" ceph dashboard ac-user-set-roles "$ACCOUNT" "$ACCOUNT" >/dev/null
else
  "${TOOLS[@]}" ceph dashboard ac-user-create "$ACCOUNT" -i /tmp/frame-osd.pass "$ACCOUNT" >/dev/null
fi
"${TOOLS[@]}" rm -f /tmp/frame-osd.pass

step "3. Secret $FRAME_NS/$SECRET"
kubectl -n "$FRAME_NS" get secret "$READ_SECRET" -o jsonpath='{.data.ca\.crt}' | base64 -d >"$WORK/ca.crt"
[ -s "$WORK/ca.crt" ] || die "le Secret $READ_SECRET n'a pas de clé ca.crt"
SVC="$SVC_NAME.$CEPH_NS.svc"
kubectl -n "$FRAME_NS" create secret generic "$SECRET" \
  --from-literal=url="https://$SVC:$PORT" \
  --from-literal=username="$ACCOUNT" \
  --from-file=password="$WORK/pass" \
  --from-file=ca.crt="$WORK/ca.crt" \
  --from-literal=serverName="$SVC" \
  --dry-run=client -o yaml | kubectl apply -f -

echo
echo "✅ Compte et Secret en place. L'assistant « Remplacer un OSD » repart seul sous une minute."
echo "   Un login refusé n'est jamais renoncé tout seul : voir docs/storage-mgr-account.md pour le débloquer."
