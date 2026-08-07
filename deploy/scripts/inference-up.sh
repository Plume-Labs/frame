#!/usr/bin/env bash
# inference-up.sh — deploy the on-GPU model server that feeds the Inference /
# KV-Cache screens. Renders and applies the `inference` namespace, the model
# server Deployment and its Service.
#
# Engine is selectable:
#   llamacpp (default) — GGUF via llama.cpp. Runs on any CUDA GPU, including
#                        Pascal (the test cluster's Tesla P4, sm_6.1).
#   vllm               — vLLM OpenAI server. REQUIRES compute capability >= 7.0,
#                        so it does NOT run on the P4 (see llm-d-up.sh). Kept
#                        selectable for a newer GPU; guarded by a preflight check.
#
# Either way the result speaks the OpenAI API on :8080 and is routed by
# llm-d-up.sh (Inference Gateway + EPP), which selects pods by label app=llamacpp.
#
# Config — all optional env vars:
#   INFER_ENGINE      llamacpp | vllm                 (default: llamacpp)
#   INFER_MODEL       model reference                 (default: per engine, below)
#   INFER_CTX         context length                  (default: 4096)
#   INFER_CACHE_SIZE  node-local weight cache limit   (default: 22Gi)
#   LLAMACPP_NGL      layers to put on the GPU        (default: 99 = all)
#   LLAMACPP_OFFLOAD  tensor-offload args             (default: -ot exps=CPU)
#   VLLM_IMAGE        vLLM image                      (default: vllm/vllm-openai:latest)
#   VLLM_EXTRA_ARGS   extra vLLM flags                (default: --gpu-memory-utilization 0.9)
#
# e.g.  INFER_MODEL=unsloth/Qwen3-30B-A3B-Instruct-2507-GGUF:Q4_K_M ./inference-up.sh
#       INFER_ENGINE=vllm INFER_MODEL=Qwen/Qwen3-30B-A3B-Instruct-2507 ./inference-up.sh
set -euo pipefail

NS=inference
ENGINE="${INFER_ENGINE:-llamacpp}"
# 4096 starved every agent: llama.cpp splits -c across its slots, so four
# concurrent agents got ~1k tokens each and lost the thread of their own work
# mid-run — malformed output and re-dispatch loops that looked like a bad model.
CTX="${INFER_CTX:-32768}"
# Slots share -c. One lane for interactive chat, one for background missions:
# with a single slot a mission monopolised the model and chat requests timed
# out; with four, nobody had enough context to finish anything.
PARALLEL="${INFER_PARALLEL:-2}"
CACHE_SIZE="${INFER_CACHE_SIZE:-22Gi}"

say() { echo -e "\n\033[1;35m==>\033[0m $*"; }

# Emits one YAML list item per argument, at the indentation the Deployment needs.
args_yaml() { for a in "$@"; do printf '            - "%s"\n' "$a"; done; }

case "$ENGINE" in
  llamacpp)
    # Qwen3-30B-A3B: MoE with only 3B active params. -ngl 99 puts the attention /
    # non-expert weights on the GPU and LLAMACPP_OFFLOAD pushes the expert tensors
    # to CPU RAM, which is what lets an ~18.5GB Q4_K_M serve from a 7.68GB P4.
    # --jinja enables the model's own chat template — Qwen3 tool calls need it,
    # and tool calling is what the delegating agents depend on.
    #
    # Offload default is `-ncmoe 34`: keep the experts of the first 34 of 48
    # layers on the CPU, leaving 14 layers' experts on the GPU. Measured on the
    # P4 against the previous `-ot exps=CPU` (which offloaded *every* expert and
    # left ~6GB of VRAM idle): VRAM 1539MiB -> 6493MiB, and a delegation call
    # 55s -> 36s cold / 23s warm, with prefill 30s -> 2.3s warm.
    # Raise N if a larger model or context overflows VRAM.
    # Do NOT add `--load-mode none`: it segfaults (exit 139) loading this model.
    MODEL="${INFER_MODEL:-unsloth/Qwen3-30B-A3B-Instruct-2507-GGUF:Q4_K_M}"
    NGL="${LLAMACPP_NGL:-99}"
    # 42, not 34: measured on the P4 with -c 32768, whose larger KV cache needs
    # the VRAM that 8 more layers of experts would have taken.
    OFFLOAD="${LLAMACPP_OFFLOAD:--ncmoe 42}"
    IMAGE="ghcr.io/ggml-org/llama.cpp:server-cuda"
    CACHE_ENV="LLAMA_CACHE"
    # $OFFLOAD is intentionally unquoted: it must word-split into separate args.
    # shellcheck disable=SC2086
    ARGS=$(args_yaml -hf "$MODEL" --host 0.0.0.0 --port 8080 \
      -ngl "$NGL" $OFFLOAD --jinja --metrics -c "$CTX" --parallel "$PARALLEL")
    ;;
  vllm)
    MODEL="${INFER_MODEL:-Qwen/Qwen3-30B-A3B-Instruct-2507}"
    IMAGE="${VLLM_IMAGE:-vllm/vllm-openai:latest}"
    CACHE_ENV="HF_HOME"
    EXTRA="${VLLM_EXTRA_ARGS:---gpu-memory-utilization 0.9}"
    # Preflight: vLLM needs sm_70+. The GPU operator labels nodes with the
    # compute capability, so refuse early and loudly rather than CrashLoop.
    MAJOR=$(kubectl get nodes -o jsonpath='{.items[*].metadata.labels.nvidia\.com/gpu\.compute\.major}' 2>/dev/null | tr ' ' '\n' | sort -rn | head -1)
    if [ -n "$MAJOR" ] && [ "$MAJOR" -lt 7 ]; then
      echo "ERROR: INFER_ENGINE=vllm needs GPU compute capability >= 7.0, cluster has ${MAJOR}.x (Pascal)." >&2
      echo "       Use INFER_ENGINE=llamacpp on this hardware." >&2
      exit 1
    fi
    # shellcheck disable=SC2086
    ARGS=$(args_yaml --model "$MODEL" --port 8080 --max-model-len "$CTX" $EXTRA)
    ;;
  *)
    echo "ERROR: unknown INFER_ENGINE='$ENGINE' (expected 'llamacpp' or 'vllm')" >&2
    exit 1
    ;;
esac

say "Inference server ($ENGINE, model=$MODEL)"

# The label stays app=llamacpp for both engines on purpose: it is the selector
# the Service, the InferencePool and llm-d-up.sh's EPP all route on. Renaming it
# per engine would silently detach the routing layer.
kubectl apply -f - <<YAML
apiVersion: v1
kind: Namespace
metadata: { name: ${NS} }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: llamacpp, namespace: ${NS}, labels: { app: llamacpp } }
spec:
  replicas: 1
  selector: { matchLabels: { app: llamacpp } }
  # Single GPU + replicas:1: RollingUpdate would surge a second pod that waits
  # forever for the one GPU the old pod still holds. Recreate frees it first.
  strategy: { type: Recreate }
  template:
    metadata: { labels: { app: llamacpp, engine: ${ENGINE} } }
    spec:
      runtimeClassName: nvidia
      containers:
        - name: server
          image: ${IMAGE}
          args:
${ARGS}
          ports: [{ containerPort: 8080, name: http }]
          env:
            - { name: ${CACHE_ENV}, value: /models }
          resources:
            limits: { nvidia.com/gpu: 1 }
          volumeMounts: [{ name: models, mountPath: /models }]
          readinessProbe:
            httpGet: { path: /health, port: 8080 }
            initialDelaySeconds: 20
            periodSeconds: 10
            failureThreshold: 120
      volumes:
        - name: models
          # Node-local on purpose. A re-downloadable ~18.5GB weight cache must not
          # sit on 3x-replicated ceph-rbd: a 25Gi PVC of it consumed ~55GB raw and
          # filled the entire Ceph cluster, blocking writes for every workload.
          # Cost of node-local: a re-download whenever the pod moves nodes.
          #
          # REQUIREMENT: the GPU node needs a root disk sized for this cache ON TOP
          # of the usual image churn. On the default 51GB the cache plus ~23GB of
          # container images drove the node to DiskPressure, which evicted pods and
          # failed unrelated image pulls with ENOSPC. Budget the model size + 40GB;
          # the test cluster's GPU node was grown to 119GB.
          # A PVC, not an emptyDir: an emptyDir dies with the pod, so every
          # restart re-downloaded ~18.5GB and left the assistant without
          # inference for ~25 minutes — twice in one morning during routine
          # config changes. local-path keeps the same node-local guarantee (no
          # Ceph, no replication, no network on the read path) while surviving
          # the pod. The re-download on a node move is unchanged.
          persistentVolumeClaim: { claimName: llamacpp-models }
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: llamacpp-models, namespace: ${NS} }
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: local-path
  resources:
    requests: { storage: ${CACHE_SIZE} }
---
apiVersion: v1
kind: Service
metadata: { name: llamacpp, namespace: ${NS} }
spec:
  selector: { app: llamacpp }
  ports: [{ port: 8080, targetPort: 8080, name: http }]
YAML
