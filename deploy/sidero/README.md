# Sidero Metal integration for Frame

Sidero Metal is the Kubernetes-native bare-metal provisioner for Talos clusters.

## Architecture in Frame

- Sidero runs in-cluster as an operator.
- New servers boot via PXE into Talos maintenance mode.
- Sidero auto-discovers machines and creates `Server` CRDs.
- `ServerClass` resources map discovered hardware profiles (GPU/RDMA/base worker) to Talos config patches.
- `Environment` resources reference Talos kernel/initramfs assets produced by Image Factory schematics.

This model replaces Ansible PXE/hot-add playbooks with declarative Kubernetes resources.

## Relationship with Talos MachineConfigs

The base MachineConfigs and patch files in `deploy/talos/` remain the source of truth for node-level behavior. Sidero `ServerClass` objects attach role-specific labels and patches during discovery/provisioning.
