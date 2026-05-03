# RDMA Networking Configuration

This directory contains Kubernetes manifests for enabling RDMA (Remote Direct Memory Access) networking in the cluster.

## Overview

RDMA provides low-latency, high-throughput network communication by allowing direct memory access between nodes without CPU involvement. This is critical for:
- AI/ML workloads with distributed training
- High-performance computing (HPC)
- Distributed storage (Ceph replication)
- Low-latency microservices

## Supported Technologies

### InfiniBand
- Native RDMA protocol
- Highest performance
- Requires InfiniBand switches and adapters
- Common adapters: Mellanox ConnectX-5/6/7

### RoCE (RDMA over Converged Ethernet)
- RDMA over Ethernet
- Uses standard Ethernet hardware (with DCB/PFC)
- Lower cost than InfiniBand
- RoCEv2 recommended (UDP-based)

### iWARP
- RDMA over TCP/IP
- Works on standard Ethernet
- Higher latency than InfiniBand/RoCE
- Better compatibility

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                    Kubernetes Node                        │
├──────────────────────────────────────────────────────────┤
│                                                            │
│  ┌─────────────────────────────────────────────────────┐ │
│  │                     Pod                              │ │
│  │  ┌────────────────────────────────────────────────┐ │ │
│  │  │         Application                             │ │ │
│  │  └────────────────────────────────────────────────┘ │ │
│  │                      │                               │ │
│  │                      ▼                               │ │
│  │  ┌────────────────────────────────────────────────┐ │ │
│  │  │      RDMA Verbs (libibverbs)                   │ │ │
│  │  └────────────────────────────────────────────────┘ │ │
│  └──────────────────────────│──────────────────────────┘ │
│                              ▼                             │
│  ┌────────────────────────────────────────────────────┐  │
│  │   RDMA Device Plugin (rdma/hca resource)           │  │
│  └────────────────────────────────────────────────────┘  │
│                              │                             │
│                              ▼                             │
│  ┌────────────────────────────────────────────────────┐  │
│  │   Multus CNI (secondary network attachment)        │  │
│  └────────────────────────────────────────────────────┘  │
│                              │                             │
│                              ▼                             │
│  ┌────────────────────────────────────────────────────┐  │
│  │   mlx5_core driver (Mellanox RDMA NIC)             │  │
│  └────────────────────────────────────────────────────┘  │
│                              │                             │
└──────────────────────────────┼─────────────────────────────┘
                               │
                               ▼
                    ┌──────────────────────┐
                    │  InfiniBand / RoCE   │
                    │      Fabric          │
                    └──────────────────────┘
```

## Components

### 1. RDMA Device Plugin
Exposes RDMA devices as Kubernetes resources. Pods can request RDMA devices like CPU/memory.

### 2. Multus CNI
Enables multiple network interfaces per pod. Pods get:
- eth0: Standard Kubernetes networking (Calico)
- net1: RDMA network interface (InfiniBand/RoCE)

### 3. Network Tuning
Optimizes kernel parameters for RDMA performance.

## Deployment

### Prerequisites

1. Install RDMA drivers on all nodes:
```bash
# For Mellanox adapters
apt-get install -y mlnx-ofed-all
modprobe mlx5_core
modprobe ib_core
```

2. Verify RDMA devices:
```bash
ibv_devices
ibstat
```

### Deploy RDMA Networking

```bash
kubectl apply -k deploy/networking/
```

This deploys:
- Multus CNI DaemonSet
- RDMA Device Plugin DaemonSet
- Network Attachment Definitions
- Kernel tuning DaemonSet

### Verify Deployment

```bash
kubectl get daemonset -n kube-system rdma-device-plugin
kubectl get network-attachment-definitions -A
```

Check RDMA resources on nodes:
```bash
kubectl describe node <node-name> | grep rdma
```

Expected output:
```
rdma/hca: 1000
```

## Usage

### Request RDMA in Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rdma-test
  annotations:
    k8s.v1.cni.cncf.io/networks: rdma-network
spec:
  containers:
    - name: app
      image: mellanox/rping-test
      resources:
        requests:
          rdma/hca: 1
        limits:
          rdma/hca: 1
      securityContext:
        capabilities:
          add:
            - IPC_LOCK
```

### RDMA Performance Test

Deploy test pods:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: rdma-server
  annotations:
    k8s.v1.cni.cncf.io/networks: rdma-network
spec:
  containers:
    - name: ib-write-bw
      image: mellanox/ib-write-bw
      command: ["ib_write_bw"]
      resources:
        limits:
          rdma/hca: 1
---
apiVersion: v1
kind: Pod
metadata:
  name: rdma-client
  annotations:
    k8s.v1.cni.cncf.io/networks: rdma-network
spec:
  containers:
    - name: ib-write-bw
      image: mellanox/ib-write-bw
      command: ["ib_write_bw", "<server-ip>"]
      resources:
        limits:
          rdma/hca: 1
```

Expected results:
- Bandwidth: 80-100 Gbps (100GbE)
- Latency: < 5 microseconds

## Network Attachment Definitions

### InfiniBand Network

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: rdma-network
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "type": "macvlan",
      "master": "ib0",
      "mode": "bridge",
      "ipam": {
        "type": "whereabouts",
        "range": "192.168.100.0/24"
      }
    }
```

### RoCE Network

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: roce-network
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "type": "ipvlan",
      "master": "enp1s0f0",
      "mode": "l2",
      "ipam": {
        "type": "whereabouts",
        "range": "192.168.101.0/24"
      }
    }
```

## Performance Tuning

### Kernel Parameters

Applied by `rdma-network-tuning.yaml`:

```bash
# Socket buffers
net.core.rmem_max = 536870912        # 512 MB
net.core.wmem_max = 536870912        # 512 MB

# TCP buffers
net.ipv4.tcp_rmem = 4096 87380 536870912
net.ipv4.tcp_wmem = 4096 65536 536870912

# Queue depth
net.core.netdev_max_backlog = 250000
```

### NIC Configuration

```bash
# Enable flow control (for RoCE)
ethtool -A enp1s0f0 rx on tx on

# Set ring buffer size
ethtool -G enp1s0f0 rx 8192 tx 8192

# Enable hardware offloads
ethtool -K enp1s0f0 gro on gso on tso on
```

### SR-IOV (Optional)

For better isolation and performance:

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovNetworkNodePolicy
metadata:
  name: rdma-sriov-policy
  namespace: sriov-network-operator
spec:
  nodeSelector:
    rdma: "true"
  resourceName: rdmanic
  priority: 99
  numVfs: 8
  nicSelector:
    vendor: "15b3"
    deviceID: "1017"
  deviceType: netdevice
  isRdma: true
```

## Monitoring

### Check RDMA Statistics

```bash
kubectl exec -it <pod-name> -- cat /sys/class/infiniband/mlx5_0/ports/1/counters/*
```

### Prometheus Metrics

RDMA metrics are exported by node-exporter:
- `node_infiniband_*` metrics
- Port state, errors, traffic

## Troubleshooting

### Device Plugin Not Starting

Check driver is loaded:
```bash
lsmod | grep mlx5
```

Check devices are visible:
```bash
ls -l /dev/infiniband/
```

### Pod Cannot Access RDMA

Check security context:
```yaml
securityContext:
  capabilities:
    add:
      - IPC_LOCK  # Required for memory pinning
```

Check device is allocated:
```bash
kubectl describe pod <pod-name> | grep rdma
```

### Low Performance

Check link state:
```bash
ibstat
```

Check for errors:
```bash
ibv_devinfo
```

Test raw device performance:
```bash
ib_write_bw
ib_send_bw
ib_read_bw
```

## Security

RDMA bypasses kernel networking stack, requiring careful security:

1. **Network Isolation**: Use dedicated VLAN for RDMA traffic
2. **Resource Limits**: Limit rdma/hca resources per namespace
3. **Capability Controls**: Only grant IPC_LOCK to trusted workloads
4. **Device Access**: Use RBAC to control pod scheduling on RDMA nodes

## References

- [Mellanox OFED Documentation](https://docs.nvidia.com/networking/display/MLNXOFEDv531000)
- [Kubernetes RDMA Device Plugin](https://github.com/Mellanox/k8s-rdma-shared-dev-plugin)
- [Multus CNI](https://github.com/k8snetworkplumbingwg/multus-cni)
- [SR-IOV Network Operator](https://github.com/k8snetworkplumbingwg/sriov-network-operator)
