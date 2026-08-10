# Getting Started

## Prerequisites

### Operator workstation

| Tool | Version |
|---|---|
| `kubectl` | 1.28+ |
| `kustomize` | 5.0+ (or `kubectl kustomize`) |
| `talosctl` | 1.9+ |
| Node.js | 20+ |
| Go | 1.25+ |
| Docker | any recent version |

### Bare-metal nodes (for a real cluster)

- RDMA-capable NIC (InfiniBand HCA or RoCE NIC)
- PXE boot support (UEFI or legacy BIOS)
- IPMI / BMC for remote power management

---

## Option A — UI against a live cluster

This is the standard dev loop: run the UI on your workstation, point it at a cluster that already has the Frame CRDs installed.

```bash
# 1. Install CRDs onto the cluster
kubectl apply -k config/crd/bases

# 2. Proxy the K8s API locally (keep this running)
kubectl proxy --port=8001

# 3. In another terminal, start the UI
npm install
npm run dev   # → http://localhost:5173
```

Vite proxies all `/apis/*` requests to `localhost:8001`, so the UI reads and writes real `FrameJob`, `FrameNode`, and `SchedulingPolicy` CRs.

To also run the operator locally:

```bash
make run   # runs controller against your current kubeconfig
```

---

## Option B — Full operator stack (envtest / Kind)

For developing or testing the Go operator without a real cluster:

```bash
# Run the envtest suite (downloads envtest binaries on first run)
make test

# Or bring up a Kind cluster and install everything
make setup-test-e2e   # creates a Kind cluster named frame-test-e2e
kubectl apply -k config/default
```

---

## Option C — Deploy to a test cluster

See [deployment.md](deployment.md) for the full walkthrough. The short version:

```bash
# 1. Build and push the UI image
make docker-build docker-push IMG=<your-registry>/frame-ui:dev

# 2. Install CRDs + operator
kubectl apply -k config/default

# 3. Deploy UI (dev overlay — 1 replica, debug logging)
kustomize build deploy/kubernetes/overlays/development | kubectl apply -f -
```

---

## First steps after install

1. **Check the operator is running:**
   ```bash
   kubectl -n frame-system get pods
   kubectl -n frame-system logs deploy/frame-controller-manager
   ```

2. **Apply a sample CR:**
   ```bash
   kubectl apply -f config/samples/frame_v1beta1_framejob.yaml
   kubectl describe framejob <name>   # see conditions + events
   ```

3. **Open the UI** at the ingress or port-forward:
   ```bash
   kubectl -n cluster-control port-forward svc/cluster-control-ui 8080:8080
   # → http://localhost:8080
   ```

4. **Submit a job from the SDK:**
   ```typescript
   import { FrameClient } from '@/lib/frame-sdk'
   const frame = new FrameClient()
   const job = await frame.jobs.submit({ name: 'test-job', pipeline: 'training', gpuCount: 1 })
   ```

---

## Troubleshooting

**`/apis/frame.plume-labs.io` returns 404**
→ CRDs not installed. Run `kubectl apply -k config/crd/bases`.

**UI shows "degraded" status**
→ `kubectl proxy` not running, or UI not pointed at the cluster. Restart `kubectl proxy --port=8001`.

**Webhook errors on CR apply**
→ cert-manager not installed or not ready. Check `kubectl -n cert-manager get pods`.

**Operator CrashLoopBackOff**
→ Check RBAC: `kubectl -n frame-system logs deploy/frame-controller-manager` and verify `ClusterRole` binding is applied (`kubectl apply -k config/rbac`).
