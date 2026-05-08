# Deployment Scripts

Utility scripts for cluster management and operations.

## Available Scripts

### bootstrap-cluster.sh
Complete cluster bootstrap from bare metal to running applications.

**Usage:**
```bash
./bootstrap-cluster.sh
```

**Environment Variables:**
- `CLUSTER_NAME` - Name of the cluster (default: bare-metal-cluster)

**What it does:**
1. Validates prerequisites (ansible, kubectl, flux)
2. Tests connectivity to all nodes
3. Sets up PXE boot server
4. Deploys Kubernetes cluster
5. Configures RDMA networking
6. Deploys Ceph storage
7. Initializes GitOps with Flux
8. Deploys Cluster Control UI

**Duration:** ~30-60 minutes depending on cluster size

---

### bootstrap-talos.sh
Talos-native cluster bootstrap from bare metal to GitOps reconciliation.

**Usage:**
```bash
./bootstrap-talos.sh <controlplane-ip> <worker-ips-comma-separated>
```

**Environment Variables:**
- `CLUSTER_NAME` - Talos cluster name (default: `frame-cluster`)
- `GITHUB_OWNER` - GitHub org/user for Flux bootstrap (**required**)
- `GITHUB_REPOSITORY` - GitHub repository for Flux bootstrap (**required**)
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
Dynamically add a new node to the cluster without downtime.

**Usage:**
```bash
./hot-add-node.sh <node-name> <node-ip> [node-type]
```

**Example:**
```bash
./hot-add-node.sh worker-05 192.168.1.25 worker
```

**What it does:**
1. Triggers PXE boot via IPMI (if configured)
2. Waits for node to be reachable
3. Configures node with Ansible
4. Joins node to Kubernetes cluster
5. Labels and taints node appropriately
6. Verifies node health

**Duration:** ~15-30 minutes

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
# Ansible
pip install ansible>=2.15

# kubectl
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl
sudo mv kubectl /usr/local/bin/

# talosctl
curl -sL https://talos.dev/install | sh

# Flux CLI
curl -s https://fluxcd.io/install.sh | sudo bash
```

2. **Configure inventory:**
Edit `deploy/ansible/inventory/production.yml` with your node IPs and credentials.

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
- Ansible inventory configuration

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
    ./scripts/bootstrap-cluster.sh
  env:
    KUBECONFIG: ${{ secrets.KUBECONFIG }}
```

### GitLab CI Example
```yaml
deploy:
  script:
    - cd deploy
    - ./scripts/bootstrap-cluster.sh
  only:
    - main
```

## Customization

Scripts can be customized by editing:
- Ansible variables in `inventory/production.yml`
- Kubernetes manifests in `kubernetes/overlays/`
- Ceph configuration in `ceph/cluster.yaml`
- Network settings in `networking/`

## Logs

All scripts output to stdout/stderr. Redirect to file for later review:
```bash
./bootstrap-cluster.sh 2>&1 | tee bootstrap.log
```
