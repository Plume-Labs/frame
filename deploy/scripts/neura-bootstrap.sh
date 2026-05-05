#!/usr/bin/env bash
# neura-bootstrap.sh — Bootstrap all HPC + Mainframe enhancements for the Neura platform
# Installs components in dependency order:
#   GPU Operator → MIG config → Volcano → Cilium → Alluxio → Redis →
#   Velero → Checkpoint Controller → Monitoring (Jaeger + DCGM + OpenLineage) → Argo Workflows
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"
NAMESPACE_TIMEOUT="${NAMESPACE_TIMEOUT:-120s}"
DEPLOY_TIMEOUT="${DEPLOY_TIMEOUT:-300s}"

info()  { echo -e "\033[1;34m[INFO]\033[0m  $*"; }
ok()    { echo -e "\033[1;32m[OK]\033[0m    $*"; }
warn()  { echo -e "\033[1;33m[WARN]\033[0m  $*"; }
die()   { echo -e "\033[1;31m[ERROR]\033[0m $*" >&2; exit 1; }

wait_for_deploy() {
  local ns="$1" name="$2"
  info "Waiting for deployment $ns/$name …"
  kubectl rollout status deployment/"$name" -n "$ns" --timeout="$DEPLOY_TIMEOUT" || warn "Timeout waiting for $ns/$name"
}

wait_for_ds() {
  local ns="$1" name="$2"
  info "Waiting for daemonset $ns/$name …"
  kubectl rollout status daemonset/"$name" -n "$ns" --timeout="$DEPLOY_TIMEOUT" || warn "Timeout waiting for $ns/$name"
}

check_prereqs() {
  info "Checking prerequisites …"
  for cmd in kubectl helm; do
    command -v "$cmd" &>/dev/null || die "Required tool not found: $cmd"
  done
  kubectl cluster-info &>/dev/null || die "kubectl cannot reach the cluster"
  ok "Prerequisites OK"
}

step1_gpu_operator() {
  info "=== Step 1: NVIDIA GPU Operator ==="
  kubectl apply -f "$DEPLOY_DIR/kubernetes/base/gpu-mig-config.yaml" || warn "GPU Operator requires Helm chart — applying ConfigMaps only"
  
  if helm repo list | grep -q nvidia; then
    info "NVIDIA Helm repo already added"
  else
    helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
    helm repo update
  fi

  helm upgrade --install gpu-operator nvidia/gpu-operator \
    --namespace gpu-operator \
    --create-namespace \
    --set migManager.enabled=true \
    --set migManager.config.name=mig-parted-config \
    --set dcgmExporter.enabled=false \
    --timeout "$DEPLOY_TIMEOUT" \
    --wait || warn "GPU Operator install failed — continuing (may not have GPU nodes)"

  ok "GPU Operator deployed"
}

step2_volcano_scheduler() {
  info "=== Step 2: Volcano Scheduler ==="
  kubectl apply -f "$DEPLOY_DIR/kubernetes/scheduling/priority-classes.yaml"
  kubectl apply -f "$DEPLOY_DIR/kubernetes/scheduling/volcano-scheduler.yaml"

  # Create service namespaces
  for ns in neura-inference neura-database neura-training; do
    kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  done

  kubectl apply -f "$DEPLOY_DIR/kubernetes/base/service-classes.yaml"
  wait_for_deploy volcano-system volcano-scheduler
  ok "Volcano Scheduler + PriorityClasses deployed"
}

step3_cilium() {
  info "=== Step 3: Cilium CNI ==="
  # Only proceed if Cilium is not already the CNI
  if kubectl get ds -n cilium cilium &>/dev/null; then
    ok "Cilium already installed — skipping"
    return
  fi

  if helm repo list | grep -q cilium; then
    info "Cilium Helm repo already added"
  else
    helm repo add cilium https://helm.cilium.io/
    helm repo update
  fi

  helm upgrade --install cilium cilium/cilium \
    --version 1.15.3 \
    --namespace cilium \
    --create-namespace \
    --set routingMode=native \
    --set enableXTSocketFallback=false \
    --set bandwidthManager.enabled=true \
    --set hubble.enabled=true \
    --set kubeProxyReplacement=strict \
    --timeout "$DEPLOY_TIMEOUT" \
    --wait || warn "Cilium install failed — ensure existing CNI is removed first"

  ok "Cilium deployed"
}

step4_networking() {
  info "=== Step 4: High-Performance Networking (SR-IOV, DPDK, RDMA) ==="
  kubectl apply -f "$DEPLOY_DIR/networking/rdma-network-tuning.yaml"
  kubectl apply -f "$DEPLOY_DIR/networking/sriov-device-plugin.yaml"
  kubectl apply -f "$DEPLOY_DIR/networking/dpdk-init.yaml"
  kubectl apply -f "$DEPLOY_DIR/networking/network-attachment-definitions.yaml"
  kubectl apply -f "$DEPLOY_DIR/networking/rdma-device-plugin.yaml"
  wait_for_ds kube-system kube-sriov-device-plugin-amd64
  ok "Networking stack deployed"
}

step5_storage() {
  info "=== Step 5: NVMe StorageClass + Data Fabric ==="
  kubectl apply -f "$DEPLOY_DIR/kubernetes/base/storageclass-nvme.yaml"
  kubectl apply -f "$DEPLOY_DIR/storage/data-fabric.yaml"
  kubectl apply -f "$DEPLOY_DIR/storage/metacat.yaml"

  # Wait for MinIO to be ready before configuring Alluxio underfs
  wait_for_deploy data-fabric minio || wait_for_ds data-fabric minio || true
  ok "Storage layer deployed"
}

step6_caching() {
  info "=== Step 6: Distributed Cache (Alluxio + Redis) ==="
  kubectl apply -f "$DEPLOY_DIR/caching/alluxio.yaml"
  kubectl apply -f "$DEPLOY_DIR/caching/redis-cluster.yaml"
  wait_for_deploy alluxio alluxio-master || true
  wait_for_ds alluxio alluxio-worker || true
  ok "Distributed cache deployed"
}

step7_velero() {
  info "=== Step 7: Velero Backup & Snapshot ==="
  if helm repo list | grep -q vmware-tanzu; then
    info "Velero Helm repo already added"
  else
    helm repo add vmware-tanzu https://vmware-tanzu.github.io/helm-charts
    helm repo update
  fi

  helm upgrade --install velero vmware-tanzu/velero \
    --namespace velero \
    --create-namespace \
    --set-json 'configuration.backupStorageLocation[0].name=default' \
    --set-json 'configuration.backupStorageLocation[0].provider=aws' \
    --set-json 'configuration.backupStorageLocation[0].bucket=neura-velero-backups' \
    --set-json 'configuration.backupStorageLocation[0].config.s3Url=http://minio-lb.data-fabric.svc.cluster.local:9000' \
    --set-json 'configuration.backupStorageLocation[0].config.s3ForcePathStyle=true' \
    --set features=EnableCSI \
    --timeout "$DEPLOY_TIMEOUT" \
    --wait || warn "Velero install failed"

  kubectl apply -f "$DEPLOY_DIR/resilience/velero.yaml"
  kubectl apply -f "$DEPLOY_DIR/resilience/checkpoint-controller.yaml"
  ok "Velero + Checkpoint Controller deployed"
}

step8_monitoring() {
  info "=== Step 8: Advanced Observability (Jaeger, DCGM, OpenLineage) ==="
  kubectl apply -f "$DEPLOY_DIR/monitoring/prometheus.yaml"
  kubectl apply -f "$DEPLOY_DIR/monitoring/jaeger.yaml"
  kubectl apply -f "$DEPLOY_DIR/monitoring/dcgm-exporter.yaml"
  kubectl apply -f "$DEPLOY_DIR/monitoring/openlineage.yaml"
  kubectl apply -f "$DEPLOY_DIR/monitoring/grafana.yaml"
  kubectl apply -f "$DEPLOY_DIR/monitoring/node-exporter.yaml"
  kubectl apply -f "$DEPLOY_DIR/monitoring/kube-state-metrics.yaml"
  wait_for_deploy monitoring jaeger
  wait_for_ds monitoring dcgm-exporter || true
  ok "Monitoring stack deployed"
}

step9_argo() {
  info "=== Step 9: Argo Workflows + Job Templates ==="
  kubectl apply -f "$DEPLOY_DIR/jobs/argo-workflows.yaml"
  wait_for_deploy argo workflow-controller
  wait_for_deploy argo argo-server

  kubectl apply -f "$DEPLOY_DIR/jobs/workflow-templates/neura-inference-dag.yaml"
  kubectl apply -f "$DEPLOY_DIR/jobs/workflow-templates/neura-training-dag.yaml"
  ok "Argo Workflows + pipeline templates deployed"
}

step10_cluster_control() {
  info "=== Step 10: Cluster Control UI ==="
  kubectl apply -k "$DEPLOY_DIR/kubernetes/overlays/production"
  wait_for_deploy cluster-control cluster-control-ui
  ok "Cluster Control UI deployed"
}

main() {
  info "Neura HPC + Mainframe Bootstrap"
  info "Deploy dir: $DEPLOY_DIR"
  echo ""

  check_prereqs

  step1_gpu_operator
  step2_volcano_scheduler
  step3_cilium
  step4_networking
  step5_storage
  step6_caching
  step7_velero
  step8_monitoring
  step9_argo
  step10_cluster_control

  echo ""
  ok "=== All Neura HPC + Mainframe components deployed successfully ==="
  echo ""
  info "Service endpoints:"
  info "  Cluster Control UI : kubectl port-forward svc/cluster-control-ui 8080:80 -n cluster-control"
  info "  Argo Workflows UI   : kubectl port-forward svc/argo-server 2746:2746 -n argo"
  info "  Jaeger UI           : kubectl port-forward svc/jaeger 16686:16686 -n monitoring"
  info "  MinIO Console       : kubectl port-forward svc/minio-lb 9001:9001 -n data-fabric"
  info "  DataHub UI          : kubectl port-forward svc/datahub-frontend 9002:9002 -n data-fabric"
  info "  Marquez (Lineage)   : kubectl port-forward svc/marquez-web 3000:3000 -n monitoring"
  info "  Grafana             : kubectl port-forward svc/grafana 3001:3000 -n monitoring"
  info "  Prometheus          : kubectl port-forward svc/prometheus 9090:9090 -n monitoring"
}

main "$@"
