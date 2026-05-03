# Ansible Playbooks

Ansible playbooks for provisioning and managing the bare metal Kubernetes cluster.

## Directory Structure

```
ansible/
├── inventory/
│   └── production.yml     # Inventory with all cluster nodes
├── playbooks/
│   ├── k8s-cluster.yml    # Main cluster deployment
│   ├── pxe-bootstrap.yml  # PXE server setup
│   └── hot-add-node.yml   # Hot-add new nodes
└── roles/                 # (to be created for each component)
```

## Prerequisites

```bash
pip install ansible>=2.15
ansible-galaxy collection install kubernetes.core
ansible-galaxy collection install ansible.posix
```

## Inventory Configuration

Edit `inventory/production.yml` and configure:
- Control plane node IPs
- Worker node IPs
- IPMI/BMC addresses for each node
- RDMA interface names
- Ceph OSD device paths

## Playbooks

### Bootstrap Entire Cluster

```bash
ansible-playbook -i inventory/production.yml playbooks/k8s-cluster.yml
```

This playbook will:
1. Configure all nodes with required packages
2. Install and configure container runtime
3. Install RDMA drivers
4. Initialize Kubernetes control plane
5. Join worker nodes
6. Deploy CNI (Calico)
7. Configure Ceph storage
8. Deploy monitoring stack

### Setup PXE Boot Server

```bash
ansible-playbook -i inventory/production.yml playbooks/pxe-bootstrap.yml
```

### Hot Add a Node

```bash
ansible-playbook -i inventory/production.yml playbooks/hot-add-node.yml \
  -e "new_node_hostname=worker-05" \
  -e "new_node_ip=192.168.1.25" \
  -e "node_type=worker"
```

## Roles (To Be Implemented)

The following roles should be created for modular configuration:

- `common` - Base system configuration
- `kernel-tuning` - Kernel parameters for performance
- `rdma-drivers` - RDMA/InfiniBand driver installation
- `container-runtime` - containerd/CRI-O setup
- `kubernetes-packages` - kubeadm, kubelet, kubectl
- `kubernetes-control-plane` - Control plane initialization
- `kubernetes-worker` - Worker node join
- `cni-calico` - Calico CNI deployment
- `rdma-device-plugin` - RDMA device plugin
- `multus-cni` - Multus for multiple network interfaces
- `ceph-common` - Ceph common packages
- `ceph-osd` - Ceph OSD configuration
- `ceph-mon` - Ceph monitor setup
- `prometheus-operator` - Prometheus installation
- `grafana` - Grafana installation
- `cluster-control-ui` - Monitoring UI deployment

## Variables

Key variables in `inventory/production.yml`:

```yaml
kubernetes_version: "1.28.5"
container_runtime: containerd
cni_plugin: calico
rdma_enabled: true
ceph_enabled: true
monitoring_enabled: true
```

## Security

Store sensitive variables in Ansible Vault:

```bash
ansible-vault create inventory/vault.yml
```

Add passwords, tokens, and secrets:

```yaml
vault_grafana_password: secure_password
vault_ipmi_password: ipmi_password
```

Use in playbooks:

```bash
ansible-playbook -i inventory/production.yml playbooks/k8s-cluster.yml --ask-vault-pass
```

## Testing

Test connectivity before running playbooks:

```bash
ansible all -i inventory/production.yml -m ping
```

## Troubleshooting

Check logs on remote nodes:

```bash
ansible workers -i inventory/production.yml -a "journalctl -u kubelet -n 50"
```

Gather facts:

```bash
ansible all -i inventory/production.yml -m setup
```
