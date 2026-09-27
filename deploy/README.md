# GitOps Deployment Infrastructure

This directory contains all Infrastructure as Code (IaC) for deploying the bare-metal Kubernetes cluster with RDMA networking, Ceph distributed storage, and the Cluster Control monitoring interface.

## Directory Structure

```
deploy/
├── kubernetes/           # K8s manifests for the monitoring app
├── gitops/              # Flux/ArgoCD configurations
├── ceph/                # Ceph cluster configurations
├── networking/          # RDMA and network fabric configs
├── monitoring/          # Observability stack (Prometheus, Grafana)
└── scripts/             # Utility scripts for deployment
```

## Prerequisites

- Bare metal servers with:
  - RDMA-capable NICs (InfiniBand or RoCE)
  - Redfish BMC access (machines are registered as FrameMachine and
    installed with Debian through FrameInstall — see docs/deployment.md)
- Control plane with:
  - kubectl 1.28+
  - Flux CLI 2.2+ or ArgoCD CLI 2.9+

## Quick Start

### 1. Install the machines

Register each machine's BMC as a `FrameMachine` and install it with a
`FrameInstall` (Debian preseed, k3s join) — see docs/deployment.md,
"Provisioning a machine with Debian".

### 2. Initialize GitOps

```bash
cd deploy/gitops
./bootstrap-flux.sh
```

### 3. Deploy Ceph Storage

```bash
kubectl apply -k deploy/ceph/
```

### 4. Deploy Monitoring UI

```bash
kubectl apply -k deploy/kubernetes/overlays/production/
```

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                     Control Plane                            │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                  │
│  │  etcd    │  │ API Srv  │  │ Flux/Argo│                  │
│  └──────────┘  └──────────┘  └──────────┘                  │
└─────────────────────────────────────────────────────────────┘
                          │
            ┌─────────────┼─────────────┐
            │             │             │
┌───────────▼──┐  ┌──────▼──────┐  ┌──▼──────────┐
│ Worker Node  │  │ Worker Node │  │ Worker Node │
│ ┌──────────┐ │  │ ┌──────────┐│  │ ┌──────────┐│
│ │   Pod    │ │  │ │   Pod    ││  │ │   Pod    ││
│ │  RDMA    │ │  │ │  RDMA    ││  │ │  RDMA    ││
│ └──────────┘ │  │ └──────────┘│  │ └──────────┘│
│ ┌──────────┐ │  │ ┌──────────┐│  │ ┌──────────┐│
│ │   Ceph   │ │  │ │   Ceph   ││  │ │   Ceph   ││
│ │   OSD    │ │  │ │   OSD    ││  │ │   OSD    ││
│ └──────────┘ │  │ └──────────┘│  │ └──────────┘│
└──────────────┘  └─────────────┘  └─────────────┘
       │                 │                 │
       └────────RDMA Fabric────────────────┘
```

## Configuration

See individual subdirectories for detailed configuration options:
- [Kubernetes manifests](./kubernetes/README.md)
- [GitOps setup](./gitops/README.md)
- [Ceph storage](./ceph/README.md)
- [RDMA networking](./networking/README.md)
