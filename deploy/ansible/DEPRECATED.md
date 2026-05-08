# DEPRECATED: Ansible provisioning path

`deploy/ansible/` is deprecated and retained temporarily for transition only.

All new bare-metal provisioning must use the Talos-native stack:

- Talos MachineConfig and patches in `deploy/talos/`
- Sidero Metal resources in `deploy/sidero/`
- Kubernetes-native tuning/labeling components in `deploy/kubernetes/base/`

This directory will be removed in a future release once migration is complete.
