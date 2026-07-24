#!/usr/bin/env bash
# test-cluster-up.sh — bring up the full virtualized test stack (Frame + Neura +
# the components that feed the live UI screens) on a fresh k3s cluster.
#
# Prereqs: kubectl + helm reachable at the cluster; per-node node-prep.sh already
# run (cpu=host, Ceph disk, ssd burst disk, ptp_kvm/ksm/burst). See docs/test-cluster.md.
#
# Images must exist on every node's containerd BEFORE this runs:
#   docker build -t cluster-control:latest .                     # Frame UI
#   docker build -t ghcr.io/rmocq/neura-api:dev    -f ../../apps/api/Dockerfile ../..
#   docker build -t ghcr.io/rmocq/neura-client:dev -f ../../apps/client/Dockerfile ../..
#   for each: docker save <img> | ssh <node> 'sudo k3s ctr images import -'
#
# Usage:  ./deploy/scripts/test-cluster-up.sh
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

say "Sample workloads (Frame CRs, Volcano queues+job, Argo DAG)"
kubectl apply -f deploy/samples/test-cluster/workloads.yaml

# On-GPU inference server (llama.cpp on the Tesla P4) — feeds the KV-Cache /
# Inference screen. Requires the NVIDIA GPU operator (nvidia.com/gpu schedulable).
if kubectl get nodes -o jsonpath='{.items[*].status.allocatable}' | grep -q 'nvidia.com/gpu'; then
  say "Inference server (llama.cpp on GPU)"
  kubectl apply -f deploy/samples/test-cluster/inference.yaml
else
  say "No schedulable GPU — skipping inference server (KV-Cache screen will show empty)"
fi

say "Done. Frame UI: http://<any-node-ip>:30880  ·  Neura: :30881"
