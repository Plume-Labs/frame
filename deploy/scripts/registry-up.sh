#!/usr/bin/env bash
# registry-up.sh — in-cluster image registry (registry:2) for local builds.
#
# Neura images are built on the dev box (podman) and need somewhere to land
# that every node can pull from. Without this, images were ctr-imported
# per-node — worked for kubelet (IfNotPresent) but broke anything needing a
# real pull (trivy-operator scanning our own images, a node that doesn't
# happen to have the image pre-imported). NodePort 30500 + insecure trust in
# each node's /etc/rancher/k3s/registries.yaml, since it's HTTP-only.
#
# Run once per test cluster. Idempotent (kubectl apply). The registries.yaml
# step needs root on each node and a k3s restart to take effect — this
# script only prints the commands; run them yourself (or via node-prep.sh
# next time it's extended to cover this).
set -euo pipefail

say() { echo -e "\n\033[1;34m==>\033[0m $*"; }
NODE_IP="${REGISTRY_NODE_IP:?set REGISTRY_NODE_IP to the node the registry's NodePort should be reached at (e.g. the cp node)}"
NODE_PORT="${REGISTRY_NODE_PORT:-30500}"

say "registry:2 (namespace registry, ceph-rbd PVC, NodePort $NODE_PORT)"
kubectl apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata: { name: registry }
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: registry-data, namespace: registry }
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: ceph-rbd
  resources: { requests: { storage: 10Gi } }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: registry, namespace: registry }
spec:
  replicas: 1
  strategy: { type: Recreate }
  selector: { matchLabels: { app: registry } }
  template:
    metadata: { labels: { app: registry } }
    spec:
      containers:
        - name: registry
          image: registry:2
          ports: [{ containerPort: 5000 }]
          volumeMounts: [{ name: data, mountPath: /var/lib/registry }]
          resources:
            requests: { cpu: 50m, memory: 128Mi }
            limits: { cpu: 500m, memory: 512Mi }
          readinessProbe:
            httpGet: { path: /v2/, port: 5000 }
            initialDelaySeconds: 3
      volumes:
        - name: data
          persistentVolumeClaim: { claimName: registry-data }
---
apiVersion: v1
kind: Service
metadata: { name: registry, namespace: registry }
spec:
  type: NodePort
  selector: { app: registry }
  ports: [{ port: 5000, targetPort: 5000, nodePort: $NODE_PORT }]
EOF
kubectl -n registry rollout status deploy/registry --timeout=90s

cat <<NOTE

  [manual step — run on EACH k3s node, needs root]
  Trust the registry as insecure (HTTP), then restart k3s to pick it up.
  Restart the nodes ONE AT A TIME and wait for Ready before the next —
  restarting all at once takes k3s DNS/networking down cluster-wide.

      sudo mkdir -p /etc/rancher/k3s
      sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
mirrors:
  "$NODE_IP:$NODE_PORT":
    endpoint: ["http://$NODE_IP:$NODE_PORT"]
configs:
  "$NODE_IP:$NODE_PORT":
    tls: { insecure_skip_verify: true }
EOF
      sudo systemctl restart k3s          # control-plane node
      sudo systemctl restart k3s-agent    # worker nodes

  Then: podman push --tls-verify=false $NODE_IP:$NODE_PORT/<image>:<tag>
  And set image.registry: "$NODE_IP:$NODE_PORT" in values.local.yaml.

NOTE
