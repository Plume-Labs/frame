# Deployment Scripts

Utility scripts for cluster management and operations.

## Available Scripts

### health-check.sh
Comprehensive cluster health check.

**Usage:**
```bash
./health-check.sh
```

**Checks:**
- Node status and readiness
- Control plane component health
- Ceph cluster health
- Storage class availability
- RDMA device plugin status
- Network attachment definitions
- GitOps synchronization status
- Monitoring UI deployment

**Output:** Detailed status report with health indicators

---

## Prerequisites

Before running any script:

1. **Install required tools:**
```bash
# kubectl
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl
sudo mv kubectl /usr/local/bin/

# Flux CLI
curl -s https://fluxcd.io/install.sh | sudo bash
```

2. **SSH access:**
Ensure passwordless SSH access to all nodes:
```bash
ssh-copy-id root@<node-ip>
```

## Troubleshooting

### Script fails with "command not found"
Install missing dependencies listed in Prerequisites section.

### Cannot connect to nodes
Check:
- SSH access: `ssh root@<node-ip>`
- Firewall rules
- Network connectivity

### Kubernetes join fails
Check:
- Control plane is healthy: `kubectl get nodes`
- Join token is valid
- Network connectivity between nodes
- Firewall rules for K8s ports (6443, 10250, etc.)

### Ceph not healthy
Check:
- OSD status: `kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph status`
- Device availability on worker nodes
- Network connectivity on Ceph network
- Rook operator logs: `kubectl -n rook-ceph logs -l app=rook-ceph-operator`

## Customization

Scripts can be customized by editing:
- Kubernetes manifests in `kubernetes/overlays/`
- Ceph configuration in `ceph/cluster.yaml`
- Network settings in `networking/`

## Logs

All scripts output to stdout/stderr. Redirect to file for later review:
```bash
./health-check.sh 2>&1 | tee health-check.log
```
