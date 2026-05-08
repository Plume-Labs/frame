# Talos-native provisioning for Frame

This directory contains Talos MachineConfigs, patch files, and Image Factory schematics used to provision bare-metal nodes without Ansible.

## 1) Generate base Talos configs

From the repository root:

```bash
talosctl gen config frame-cluster https://<CONTROLPLANE_IP>:6443 \
  --output-dir ./deploy/talos/generated
```

## 2) Apply MachineConfig patches

Use `talosctl machineconfig patch` to merge environment-specific patches into base configs.

```bash
# Example: patch worker config with kubelet + hugepages + NUMA/IRQ tuning
talosctl machineconfig patch ./deploy/talos/worker.yaml \
  --patch @./deploy/talos/patches/kubelet-config.yaml \
  --patch @./deploy/talos/patches/hugepages.yaml \
  --patch @./deploy/talos/patches/numa-irq.yaml \
  --output ./deploy/talos/generated/worker-patched.yaml
```

## 3) Build custom Talos images with Image Factory

1. Pick a schematic in `deploy/talos/schematics/` (for example `worker-gpu.yaml`).
2. Submit it to Image Factory:

```bash
curl -sS -X POST https://factory.talos.dev/schematics \
  -H 'Content-Type: application/yaml' \
  --data-binary @./deploy/talos/schematics/worker-gpu.yaml
```

3. Use the returned schematic ID to build/download ISO, PXE, kernel, and initramfs artifacts.

## 4) New node bootstrap flow (Talos-native)

1. Build a Talos image from an Image Factory schematic.
2. Boot server via ISO/PXE into Talos maintenance mode.
3. Apply control plane or worker MachineConfig with `talosctl apply-config --insecure`.
4. Bootstrap the cluster once with `talosctl bootstrap` (control plane).
5. Fetch kubeconfig using `talosctl kubeconfig`.
6. Bootstrap Flux and let GitOps reconcile Kubernetes add-ons.
7. Use Sidero ServerClasses to classify future nodes (GPU/RDMA/CPU-only) automatically.

See `deploy/scripts/bootstrap-talos.sh` for an end-to-end bootstrap helper script.
