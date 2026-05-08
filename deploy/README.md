# GitOps Deployment Infrastructure

This directory contains all Infrastructure as Code (IaC) for deploying the bare-metal Kubernetes cluster with Talos/Sidero provisioning, RDMA networking, Ceph distributed storage, and the Cluster Control monitoring interface.

## Directory Structure

```
deploy/
├── kubernetes/           # K8s manifests for the monitoring app
├── gitops/              # Flux/ArgoCD configurations
├── talos/               # Talos MachineConfigs and schematics
├── sidero/              # Sidero Metal resources (Environment/ServerClass)
├── pxe/                 # PXE boot configurations
├── ceph/                # Ceph cluster configurations
├── networking/          # RDMA and network fabric configs
├── monitoring/          # Observability stack (Prometheus, Grafana)
└── scripts/             # Utility scripts for deployment
```

## Prerequisites

- Bare metal servers with:
  - RDMA-capable NICs (InfiniBand or RoCE)
  - PXE boot support
  - IPMI/BMC access
- Control plane with:
  - talosctl 1.9+
  - kubectl 1.28+
  - Flux CLI 2.2+ or ArgoCD CLI 2.9+

## Quick Start

### 1. Bootstrap Bare Metal Provisioning

```bash
cd deploy
./scripts/bootstrap-talos.sh <controlplane-ip> <worker-ips-comma-separated>
```

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
- [Talos provisioning](./talos/README.md)
- [Sidero Metal resources](./sidero/README.md)
- [PXE configuration](./pxe/README.md)
- [Ceph storage](./ceph/README.md)
- [RDMA networking](./networking/README.md)
