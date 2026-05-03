# GitOps Deployment Infrastructure

This directory contains all Infrastructure as Code (IaC) for deploying the bare-metal Kubernetes cluster with RDMA networking, PXE provisioning, Ceph distributed storage, and the Cluster Control monitoring interface.

## Directory Structure

```
deploy/
├── kubernetes/           # K8s manifests for the monitoring app
├── gitops/              # Flux/ArgoCD configurations
├── ansible/             # Bare metal provisioning playbooks
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
  - Ansible 2.15+
  - kubectl 1.28+
  - Flux CLI 2.2+ or ArgoCD CLI 2.9+
  - Python 3.11+

## Quick Start

### 1. Bootstrap Bare Metal Provisioning

```bash
cd deploy/ansible
./scripts/bootstrap-cluster.sh
```

### 2. Deploy Kubernetes with RDMA

```bash
ansible-playbook -i inventory/production.yml playbooks/k8s-cluster.yml
```

### 3. Initialize GitOps

```bash
cd deploy/gitops
./bootstrap-flux.sh
```

### 4. Deploy Ceph Storage

```bash
kubectl apply -k deploy/ceph/
```

### 5. Deploy Monitoring UI

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
- [Ansible playbooks](./ansible/README.md)
- [PXE configuration](./pxe/README.md)
- [Ceph storage](./ceph/README.md)
- [RDMA networking](./networking/README.md)
