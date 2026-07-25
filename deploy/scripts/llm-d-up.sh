#!/usr/bin/env bash
# llm-d-up.sh — deploy the llm-d inference routing layer in front of the
# llama.cpp model server (deploy/samples/test-cluster/inference.yaml).
#
# Why not the full llm-d stack: llm-d's serving engine is vLLM/SGLang, which
# require GPU compute capability >= 7.0. The test GPU (Tesla P4) is Pascal
# (sm_6.1), so the engine is llama.cpp. What IS Pascal-compatible and is the
# engine-agnostic heart of llm-d is the Gateway API Inference Extension
# (Inference Gateway + InferencePool + EPP endpoint-picker) — the concurrency /
# queue-aware router. Prefill/decode disaggregation + KV-cache-aware scoring are
# vLLM-only and stay inert on one Pascal GPU (EPP runs in passthrough parsing).
#
# Result: request -> Agentgateway (Inference Gateway) -> EPP -> llama.cpp.
# Point an OpenAI client at the gateway service :80 (/v1).
set -euo pipefail

NS=inference
IGW_VERSION="${IGW_VERSION:-v1.5.0}"
GWAPI_VERSION="${GWAPI_VERSION:-v1.3.0}"
AGW_VERSION="${AGW_VERSION:-v1.0.0}"
BACKEND_LABEL="${BACKEND_LABEL:-llamacpp}"   # pods to route to
BACKEND_PORT="${BACKEND_PORT:-8080}"

say() { echo -e "\n\033[1;35m==>\033[0m $*"; }

say "Gateway API CRDs ($GWAPI_VERSION) + GAIE inference CRDs ($IGW_VERSION)"
kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GWAPI_VERSION/standard-install.yaml"
kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/$IGW_VERSION/manifests.yaml"

say "Agentgateway ($AGW_VERSION) — Inference Gateway implementation"
helm upgrade -i --create-namespace -n agentgateway-system --version "$AGW_VERSION" \
  agentgateway-crds oci://cr.agentgateway.dev/charts/agentgateway-crds
helm upgrade -i -n agentgateway-system --version "$AGW_VERSION" \
  agentgateway oci://cr.agentgateway.dev/charts/agentgateway --set inferenceExtension.enabled=true
kubectl -n agentgateway-system rollout status deploy --timeout=180s

say "Inference Gateway (namespace $NS)"
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: { name: inference-gateway, namespace: $NS }
spec:
  gatewayClassName: agentgateway
  listeners:
    - { name: http, port: 80, protocol: HTTP }
EOF

say "InferencePool + EPP (routes to app=$BACKEND_LABEL:$BACKEND_PORT)"
# passthrough-parser: llama.cpp is not a vLLM-family engine, so EPP does load/
# queue routing, not KV-cache-aware scoring. EPP right-sized for a small node.
helm upgrade -i "$BACKEND_LABEL" -n "$NS" --dependency-update \
  --set inferencePool.modelServers.matchLabels.app="$BACKEND_LABEL" \
  --set "inferencePool.targetPorts[0].number=$BACKEND_PORT" \
  --set inferencePool.parser=passthrough-parser \
  --set provider.name=none \
  --set experimentalHttpRoute.enabled=true \
  --set experimentalHttpRoute.inferenceGatewayName=inference-gateway \
  --set inferenceExtension.resources.requests.cpu=200m \
  --set inferenceExtension.resources.requests.memory=256Mi \
  --set inferenceExtension.resources.limits.memory=512Mi \
  --version "$IGW_VERSION" \
  oci://registry.k8s.io/gateway-api-inference-extension/charts/inferencepool

kubectl -n "$NS" rollout status deploy/"$BACKEND_LABEL"-epp --timeout=180s
say "Done. Gateway svc: inference-gateway.$NS.svc.cluster.local:80 (NodePort $(kubectl -n $NS get svc inference-gateway -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null))"
