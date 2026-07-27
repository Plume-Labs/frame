#!/usr/bin/env bash
# test-cluster-up.sh — bring up the full virtualized test stack (Frame + Neura +
# the components that feed the live UI screens) on a fresh k3s cluster.
#
# Prereqs: kubectl + helm reachable at the cluster; per-node node-prep.sh already
# run (cpu=host, Ceph disk, ssd burst disk, ptp_kvm/ksm/burst). See docs/test-cluster.md.
#
# cluster-control-ui still needs manual ctr-import per node (see below) — it's
# not built from the same values-driven chart Neura uses. Neura's own images
# go through the in-cluster registry this script sets up (registry-up.sh),
# no per-node import needed once that's live: podman build, podman push
# --tls-verify=false <REGISTRY_NODE_IP>:30500/neura-api:<tag>, set
# image.registry in values.local.yaml.
#
# cluster-control-ui image must exist on every node's containerd BEFORE this runs:
#   docker build -t cluster-control:latest .
#   docker save cluster-control:latest | ssh <node> 'sudo k3s ctr images import -'
#
# Usage:  ./deploy/scripts/test-cluster-up.sh
#   GPU=1                         also install the NVIDIA GPU operator + llama.cpp
#   PVE_URL/PVE_USER/PVE_PASS     also stamp real rack labels from Proxmox
#   REGISTRY_NODE_IP              node IP for the in-cluster registry's NodePort (skip if unset)
#   e.g. GPU=1 PVE_URL=https://192.168.2.1:8006 PVE_PASS=… ./deploy/scripts/test-cluster-up.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."   # repo root (.externals/frame)

ROOK_VER=release-1.20
ARGO_VER=v3.7.2
VOLCANO_VER=release-1.11
CERTMGR_VER=v1.21.0
CEPH_IMG=quay.io/ceph/ceph:v20.2.2

say() { echo -e "\n\033[1;34m==>\033[0m $*"; }

say "cert-manager $CERTMGR_VER"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/$CERTMGR_VER/cert-manager.yaml"
kubectl -n cert-manager wait --for=condition=Available deploy --all --timeout=180s

say "Rook operator ($ROOK_VER) + CSI-operator CRDs (CephConnection)"
R="https://raw.githubusercontent.com/rook/rook/$ROOK_VER/deploy/examples"
kubectl apply -f "$R/crds.yaml" --server-side
kubectl apply -f "$R/common.yaml"
kubectl apply --server-side -f "$R/csi-operator.yaml"   # required by Rook 1.20 or reconcile fails
kubectl apply -f "$R/operator.yaml"
kubectl -n rook-ceph rollout status deploy/rook-ceph-operator --timeout=180s

say "Ceph cluster + block pool + storageclass"
kubectl apply -k deploy/ceph/    # pins $CEPH_IMG via deploy/ceph/cluster.yaml
echo "waiting for Ceph HEALTH_OK (OSDs form)…"
for i in $(seq 1 40); do
  [ "$(kubectl -n rook-ceph get cephcluster rook-ceph -o jsonpath='{.status.ceph.health}' 2>/dev/null)" = HEALTH_OK ] && break
  sleep 15
done

say "Frame operator (CRDs + controller + webhooks)"
kubectl apply -k config/default
kubectl -n frame-system rollout status deploy/frame-controller-manager --timeout=180s

say "Frame UI (dev overlay) + NodePort"
kubectl apply -f "https://raw.githubusercontent.com/argoproj/argo-workflows/$ARGO_VER/manifests/base/crds/full/argoproj.io_workflows.yaml" --server-side
kustomize build deploy/kubernetes/overlays/development | kubectl apply -f - || \
  kubectl kustomize deploy/kubernetes/overlays/development | kubectl apply -f -
kubectl -n cluster-control patch svc cluster-control-ui \
  -p '{"spec":{"type":"NodePort","ports":[{"port":80,"targetPort":8080,"nodePort":30880}]}}'

say "In-cluster image registry (for local Neura builds)"
if [ -n "${REGISTRY_NODE_IP:-}" ]; then
  REGISTRY_NODE_IP="$REGISTRY_NODE_IP" bash deploy/scripts/registry-up.sh || \
    echo "  (registry install failed — non-fatal; Neura images fall back to whatever's ctr-imported per node)"
else
  echo "  REGISTRY_NODE_IP not set — skipping (see deploy/scripts/registry-up.sh)"
fi

say "Neura (Helm)"
helm upgrade --install neura ../../k8s/helm/neura -n neura --create-namespace \
  -f ../../k8s/helm/neura/values.local.yaml \
  --set api.env.NODE_ENV=production --set api.env.AI_MODEL_PROVIDER=ollama

say "Alluxio (tiered storage), node-exporter (ksmd/netdev/timex)"
kubectl apply -f deploy/caching/alluxio-min.yaml
kubectl apply -f deploy/monitoring/node-exporter-test.yaml

say "Volcano ($VOLCANO_VER)"
kubectl apply -f "https://raw.githubusercontent.com/volcano-sh/volcano/$VOLCANO_VER/installer/volcano-development.yaml" --server-side --force-conflicts
kubectl -n volcano-system rollout status deploy/volcano-scheduler --timeout=180s

say "Argo Workflows ($ARGO_VER)"
kubectl create namespace argo --dry-run=client -o yaml | kubectl apply -f -
kubectl -n argo apply -f "https://github.com/argoproj/argo-workflows/releases/download/$ARGO_VER/namespace-install.yaml" --server-side --force-conflicts
kubectl -n argo rollout status deploy/workflow-controller --timeout=180s

# NVIDIA GPU operator — makes nvidia.com/gpu schedulable (device-plugin) and
# ships DCGM-exporter for the GPU screen. driver.enabled=false: the node driver
# is installed by node-prep.sh (needs Proxmox passthrough + Secure Boot OFF).
# Opt-in (GPU=1) so GPU-less clusters skip the heavy operator.
if [ "${GPU:-}" = "1" ]; then
  say "NVIDIA GPU operator (driver.enabled=false — uses the host driver)"
  helm repo add nvidia https://helm.ngc.nvidia.com/nvidia >/dev/null 2>&1 || true
  helm repo update nvidia >/dev/null 2>&1 || true
  helm upgrade --install gpu-operator nvidia/gpu-operator -n gpu-operator --create-namespace \
    --set driver.enabled=false --set toolkit.enabled=true
  echo "waiting for nvidia.com/gpu to become allocatable…"
  for i in $(seq 1 40); do
    kubectl get nodes -o jsonpath='{.items[*].status.allocatable}' | grep -q 'nvidia.com/gpu' && break
    sleep 15
  done
fi

say "Observability (Prometheus + Grafana + Alertmanager)"
bash deploy/scripts/observability-up.sh || echo "  (observability install failed — non-fatal)"

say "Disaster recovery (Ceph RGW object store + Velero backups)"
bash deploy/scripts/backup-up.sh || echo "  (backup stack failed — non-fatal)"

say "Runtime security (Falco + Falcosidekick → Frame Security screen)"
bash deploy/scripts/security-up.sh || echo "  (Falco install failed — non-fatal; Security screen will show empty)"

say "Embeddings server (TEI on CPU — Neura AI_EMBEDDING_BASE_URL)"
kubectl create namespace inference --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/samples/test-cluster/tei.yaml

say "Sample workloads (Frame CRs, Volcano queues+job, Argo DAG)"
kubectl apply -f deploy/samples/test-cluster/workloads.yaml

# On-GPU inference server (llama.cpp on the Tesla P4) — feeds the KV-Cache /
# Inference screen. Requires the NVIDIA GPU operator (nvidia.com/gpu schedulable).
if kubectl get nodes -o jsonpath='{.items[*].status.allocatable}' | grep -q 'nvidia.com/gpu'; then
  say "Inference server (llama.cpp on GPU)"
  kubectl apply -f deploy/samples/test-cluster/inference.yaml
  kubectl -n inference rollout status deploy/llamacpp --timeout=600s || true
  say "llm-d routing layer (Inference Gateway + EPP → llama.cpp)"
  bash deploy/scripts/llm-d-up.sh || echo "  (llm-d layer failed — non-fatal; llama.cpp still serves directly)"
else
  say "No schedulable GPU — skipping inference server (KV-Cache screen will show empty)"
fi

# Stamp real physical topology (hypervisor host) onto the nodes for the Racks
# screen. Needs Proxmox creds; skipped otherwise (Racks falls back to spec.rack).
if [ -n "${PVE_URL:-}" ] && [ -n "${PVE_PASS:-}" ]; then
  say "Rack labels from Proxmox host mapping"
  bash deploy/scripts/label-racks.sh || echo "  (rack labeling failed — non-fatal)"
else
  say "PVE_URL/PVE_PASS unset — skipping physical rack labels (Racks uses spec.rack)"
fi

say "Done. Frame UI: http://<any-node-ip>:30880  ·  Neura: :30881"
