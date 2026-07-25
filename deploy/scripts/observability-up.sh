#!/usr/bin/env bash
# observability-up.sh — metrics backbone: kube-prometheus-stack (Prometheus +
# Grafana + Alertmanager + kube-state-metrics) with the cluster's existing
# node-exporter reused, plus ServiceMonitors/PodMonitors for the custom
# exporters (DCGM, llama.cpp, Tetragon, Falcosidekick). Storage on Ceph RBD.
#   Grafana: NodePort 30300 (admin / neura). Prometheus TSDB retention 3d.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

say() { echo -e "\n\033[1;32m==>\033[0m $*"; }

say "kube-prometheus-stack (Prometheus + Grafana + Alertmanager)"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
helm repo update prometheus-community >/dev/null 2>&1 || true
helm upgrade -i kps prometheus-community/kube-prometheus-stack -n monitoring --create-namespace \
  -f deploy/samples/test-cluster/kps-values.yaml
kubectl -n monitoring rollout status statefulset/prometheus-kps-kube-prometheus-stack-prometheus --timeout=300s || true

say "ServiceMonitors / PodMonitors for the custom exporters"
kubectl apply -f deploy/samples/test-cluster/servicemonitors.yaml

say "Done. Grafana http://<node>:30300 (admin/neura). Prometheus scrapes node-exporter, DCGM, llama.cpp, Tetragon, Falcosidekick, kube-state-metrics, kubelet."
