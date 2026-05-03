# Cluster Control - Distributed Systems Monitor

A mainframe-inspired distributed systems monitoring platform with complete GitOps deployment infrastructure for bare-metal Kubernetes clusters.

## Overview

Cluster Control provides:
- **Real-time Monitoring UI** - Visual cluster topology, node metrics, and system events
- **Complete IaC** - Production-ready Infrastructure as Code for bare-metal Kubernetes
- **RDMA Networking** - Low-latency InfiniBand/RoCE networking for HPC workloads
- **Ceph Storage** - Distributed block and file storage with self-healing
- **PXE Provisioning** - Automated bare-metal provisioning via network boot
- **GitOps CD** - Continuous delivery with Flux CD and ArgoCD
- **Hot Node Addition** - Dynamic cluster expansion without downtime

## Quick Start

### Local Development (UI Only)

```bash
npm install
npm run dev
```

Visit http://localhost:5173

### Production Deployment

```bash
cd deploy
./scripts/bootstrap-cluster.sh
```

This will:
1. Configure PXE boot server
2. Deploy Kubernetes with RDMA networking
3. Initialize Ceph storage
4. Setup GitOps with Flux
5. Deploy monitoring UI

See [deploy/README.md](deploy/README.md) for detailed deployment instructions.

## Project Structure

```
.
├── src/                    # React monitoring UI
│   ├── components/        # UI components
│   ├── lib/              # Cluster simulation logic
│   └── hooks/            # React hooks
├── deploy/                # Infrastructure as Code
│   ├── kubernetes/       # K8s manifests (Kustomize)
│   ├── gitops/          # Flux/ArgoCD configs
│   ├── ansible/         # Bare metal provisioning
│   ├── pxe/             # PXE boot configuration
│   ├── ceph/            # Ceph storage manifests
│   ├── networking/      # RDMA networking configs
│   ├── monitoring/      # Prometheus/Grafana stack
│   └── scripts/         # Deployment utilities
├── Dockerfile           # Container image for UI
└── PRD.md              # Product requirements

```

## Features

### Monitoring UI
- Live cluster topology visualization
- Per-node resource metrics (CPU, memory, network, storage)
- System event log with filtering
- Node detail inspection
- Responsive mobile design

### Infrastructure
- **Bare Metal Provisioning**: Ansible playbooks for node configuration
- **PXE Boot**: Network-based OS installation
- **Kubernetes**: Production-ready cluster with HA control plane
- **RDMA**: InfiniBand/RoCE device plugin for low-latency networking
- **Ceph**: Distributed storage with RBD and CephFS
- **GitOps**: Automated deployments with Flux or ArgoCD
- **Monitoring**: Prometheus + Grafana for observability

## Architecture

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
│ │   Pods   │ │  │ │   Pods   ││  │ │   Pods   ││
│ │  (RDMA)  │ │  │ │  (RDMA)  ││  │ │  (RDMA)  ││
│ └──────────┘ │  │ └──────────┘│  │ └──────────┘│
│ ┌──────────┐ │  │ ┌──────────┐│  │ ┌──────────┐│
│ │   Ceph   │ │  │ │   Ceph   ││  │ │   Ceph   ││
│ │   OSD    │ │  │ │   OSD    ││  │ │   OSD    ││
│ └──────────┘ │  │ └──────────┘│  │ └──────────┘│
└──────────────┘  └─────────────┘  └─────────────┘
       │                 │                 │
       └────────RDMA Fabric────────────────┘
```

## Deployment Guides

- [Kubernetes Manifests](deploy/kubernetes/README.md)
- [GitOps Setup](deploy/gitops/README.md)
- [Ansible Playbooks](deploy/ansible/README.md)
- [PXE Configuration](deploy/pxe/README.md)
- [Ceph Storage](deploy/ceph/README.md)
- [RDMA Networking](deploy/networking/README.md)

## Technology Stack

### Frontend
- React 19 + TypeScript
- Tailwind CSS + shadcn/ui
- Framer Motion animations
- Vite build system

### Infrastructure
- Kubernetes 1.28+
- Containerd runtime
- Calico CNI + Multus
- Rook Ceph 1.13+
- Flux CD / ArgoCD
- Ansible 2.15+
- Prometheus + Grafana

### Networking
- InfiniBand / RoCE RDMA
- Mellanox OFED drivers
- RDMA Device Plugin
- Network tuning for low latency

## Scripts

All scripts are located in `deploy/scripts/`:

- `bootstrap-cluster.sh` - Complete cluster bootstrap
- `hot-add-node.sh` - Add node dynamically
- `health-check.sh` - Cluster health verification

## License

The Spark Template files and resources from GitHub are licensed under the terms of the MIT license, Copyright GitHub, Inc.

## Documentation

- [Product Requirements](PRD.md)
- [Deployment Overview](deploy/README.md)

## Support

For issues and questions:
- Check [deploy/README.md](deploy/README.md) for deployment troubleshooting
- Review component-specific READMEs in each deploy/ subdirectory
- Open an issue with detailed logs and environment info
