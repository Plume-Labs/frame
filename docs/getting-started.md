# Getting Started

## Prerequisites

### Bare-metal cluster nodes

Your node can have many roles for optimal resource utilization (e.g., compute, storage, GPU, controller).

Frame provisionning it with roles based on detected hardware and user preference. The operator will automatically detect the node's hardware and assign the appropriate role.

#### Default :
- 64-bit x86_64 CPU (Dual socket recommended for redundancy and memory bandwidth, RISC-V and ARM64 are not supported yet)
- 16+ GB ECC RAM (32+ GB recommended, be careful with memory channels for bandwidth)
- HBA/IT mode storage controller (for ZFS and CEPH storage, RAID mode is not supported)
- 100+ GB local storage (NVMe or SSD recommended, ZFS mirror is a good option for redundancy)
- 100+ GB OSD storage (NVMe or SSD recommended, the more you have, the better is performance and redundancy)
- 10+ Gbps network (RDMA-capable NIC recommended for low-latency workloads)

#### Recommended for high-performance workloads and big cluster management :
- GPU (NVIDIA has better support for now with official operator) with supported drivers
- ASIC or FPGA accelerators (if your workloads require them, no default support in Frame for now)
- 40+ Gbps RDMA-capable NIC (InfiniBand HCA or RoCE NIC, for high-throughput workloads and large clusters)
- PXE boot support (For automated node provisioning and OS installation)
- IPMI / BMC for remote power management

### Single-node cluster for testing
For testing and development, you can use a single-node cluster.

 /!\ Frame is designed for multi-node clusters. Running on a single node may lead to unexpected behavior and is not recommended for production use.

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
