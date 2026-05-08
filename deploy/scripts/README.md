# Deployment Scripts

Utility scripts for cluster management and operations.

## Available Scripts

### bootstrap-cluster.sh
Compatibility wrapper that delegates to the Talos-native bootstrap.

**Usage:**
```bash
./bootstrap-cluster.sh <controlplane-ip> <worker-ips-comma-separated>
```

**Environment Variables:**
- `CLUSTER_NAME` - Name of the cluster (default: bare-metal-cluster)

**What it does:**
1. Validates Talos/Flux prerequisites
2. Delegates to `bootstrap-talos.sh`

**Duration:** Same as `bootstrap-talos.sh`

---

### bootstrap-talos.sh
Talos-native cluster bootstrap from bare metal to GitOps reconciliation.

**Usage:**
```bash
./bootstrap-talos.sh <controlplane-ip> <worker-ips-comma-separated>
```

**Environment Variables:**
- `CLUSTER_NAME` - Talos cluster name (default: `frame-cluster`)
- `GITHUB_OWNER` - GitHub org/user for Flux bootstrap (**required**, alias: `GITHUB_USER`)
- `GITHUB_REPOSITORY` - GitHub repository for Flux bootstrap (**required**, alias: `GITHUB_REPO`)
- `GITHUB_BRANCH` - Git branch for Flux bootstrap (default: `main`)
- `GITOPS_PATH` - Flux path inside repository (default: `clusters/<cluster-name>`)

**What it does:**
1. Generates Talos configs with config patches
2. Applies control plane and worker configs with `talosctl apply-config`
3. Bootstraps Talos control plane
4. Fetches kubeconfig with `talosctl kubeconfig`
5. Bootstraps Flux with `flux bootstrap github`

---

### hot-add-node.sh
Migration helper that points to the Talos/Sidero hot-add flow.

**Usage:**
```bash
./hot-add-node.sh <node-name> <node-ip> [node-type]
```

**Example:**
```bash
./hot-add-node.sh worker-05 192.168.1.25 worker
```

**What it does:**
1. Explains the Talos/Sidero process to register and classify the node
2. Prints the requested node metadata for operator handoff

**Duration:** N/A (informational helper)

---

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

# talosctl
curl -sL https://talos.dev/install | sh

# Flux CLI
curl -s https://fluxcd.io/install.sh | sudo bash
```

2. **Set bootstrap inputs:**
Provide control plane and worker IPs as script arguments (or via `CONTROLPLANE_IP` / `WORKER_IPS`).

3. **SSH access:**
Ensure passwordless SSH access to all nodes:
```bash
ssh-copy-id root@<node-ip>
```

4. **IPMI access (for hot-add):**
Configure IPMI credentials in inventory or environment:
```bash
export IPMI_USER=admin
export IPMI_PASSWORD=password
```

## Troubleshooting

### Script fails with "command not found"
Install missing dependencies listed in Prerequisites section.

### Cannot connect to nodes
Check:
- SSH access: `ssh root@<node-ip>`
- Firewall rules
- Network connectivity
- Talos endpoint reachability

### PXE boot fails
Check:
- DHCP server configuration
- TFTP server accessibility (port 69)
- Boot order in BIOS/UEFI
- Network cable and switch port

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

## CI/CD Integration

These scripts can be integrated into CI/CD pipelines:

### GitHub Actions Example
```yaml
- name: Deploy Cluster
  run: |
    cd deploy
    ./scripts/bootstrap-talos.sh "$CONTROLPLANE_IP" "$WORKER_IPS"
  env:
    KUBECONFIG: ${{ secrets.KUBECONFIG }}
```

### GitLab CI Example
```yaml
deploy:
  script:
    - cd deploy
    - ./scripts/bootstrap-talos.sh "$CONTROLPLANE_IP" "$WORKER_IPS"
  only:
    - main
```

## Customization

Scripts can be customized by editing:
- Talos/Sidero resources under `deploy/talos/` and `deploy/sidero/`
- Kubernetes manifests in `kubernetes/overlays/`
- Ceph configuration in `ceph/cluster.yaml`
- Network settings in `networking/`

## Logs

All scripts output to stdout/stderr. Redirect to file for later review:
```bash
./bootstrap-talos.sh <controlplane-ip> <worker-ips-comma-separated> 2>&1 | tee bootstrap.log
```
