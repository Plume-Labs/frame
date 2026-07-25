#!/usr/bin/env bash
# backup-up.sh — disaster recovery: Ceph RGW object store + Velero.
# Backs up k8s resources + PVC data (kopia file-level) to a Ceph S3 bucket, so a
# node/cluster loss is recoverable. Idempotent.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

say() { echo -e "\n\033[1;36m==>\033[0m $*"; }

say "Ceph RGW object store + bucket (ObjectBucketClaim)"
kubectl create namespace velero --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/samples/test-cluster/ceph-objectstore.yaml
echo "waiting for RGW + bucket…"
for i in $(seq 1 40); do
  [ "$(kubectl -n rook-ceph get pod -l app=rook-ceph-rgw -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null)" = true ] \
    && [ "$(kubectl -n velero get obc velero-backups -o jsonpath='{.status.phase}' 2>/dev/null)" = Bound ] && break
  sleep 10
done

BUCKET=$(kubectl -n velero get cm velero-backups -o jsonpath='{.data.BUCKET_NAME}')
AK=$(kubectl -n velero get secret velero-backups -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d)
SK=$(kubectl -n velero get secret velero-backups -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}' | base64 -d)
printf '[default]\naws_access_key_id=%s\naws_secret_access_key=%s\n' "$AK" "$SK" > /tmp/velero-creds
echo "bucket: $BUCKET"

say "Velero (aws plugin → Ceph RGW S3, node-agent kopia for PVCs)"
helm repo add vmware-tanzu https://vmware-tanzu.github.io/helm-charts >/dev/null 2>&1 || true
helm repo update vmware-tanzu >/dev/null 2>&1 || true
helm upgrade -i velero vmware-tanzu/velero -n velero \
  --set-file credentials.secretContents.cloud=/tmp/velero-creds \
  --set "initContainers[0].name=velero-plugin-for-aws" \
  --set "initContainers[0].image=velero/velero-plugin-for-aws:v1.10.0" \
  --set "initContainers[0].volumeMounts[0].mountPath=/target" \
  --set "initContainers[0].volumeMounts[0].name=plugins" \
  --set configuration.backupStorageLocation[0].name=default \
  --set configuration.backupStorageLocation[0].provider=aws \
  --set configuration.backupStorageLocation[0].bucket="$BUCKET" \
  --set configuration.backupStorageLocation[0].config.region=us-east-1 \
  --set configuration.backupStorageLocation[0].config.s3ForcePathStyle=true \
  --set configuration.backupStorageLocation[0].config.s3Url=http://rook-ceph-rgw-neura-s3.rook-ceph.svc:80 \
  --set snapshotsEnabled=false \
  --set deployNodeAgent=true \
  --set configuration.defaultVolumesToFsBackup=true
rm -f /tmp/velero-creds
kubectl -n velero rollout status deploy/velero --timeout=180s

say "Daily backup schedule (neura ns, 14d retention)"
kubectl apply -f - <<'SCHED'
apiVersion: velero.io/v1
kind: Schedule
metadata: { name: daily-neura, namespace: velero }
spec:
  schedule: "0 2 * * *"
  template:
    includedNamespaces: [neura]
    defaultVolumesToFsBackup: true
    ttl: 336h0m0s
SCHED

say "Done. On-demand backup: kubectl -n velero create -f - (Backup CR), or wait for the 02:00 schedule."
