# PXE Boot

Frame nodes PXE-boot into **Talos Linux**, an immutable, API-managed OS with
no package manager, shell, or SSH. There is no Kickstart/preseed/kernel-image
step to configure here — that flow applies to traditional distros (Ubuntu,
Rocky, Debian), not Talos.

## What actually serves PXE boot

**Sidero Metal** (`deploy/sidero/`) runs in-cluster and provides its own
PXE/iPXE, TFTP, and DHCP-proxy services. It:

1. Discovers bare-metal servers that PXE-boot against it.
2. Serves them a Talos maintenance-mode image built from an Image Factory
   schematic (`deploy/sidero/environments/`).
3. Registers each as a `Server` CRD, maps it to a `ServerClass`
   (`deploy/sidero/serverclasses/`) by hardware profile (GPU/RDMA/base
   worker).
4. Applies the matching `TalosMachineConfig` from `deploy/talos/` to install
   Talos to disk and join the cluster — no manual OS install step.

See `deploy/sidero/README.md` for the full flow and `deploy/talos/README.md`
for the MachineConfig side.

## Network requirements

- Dedicated provisioning network/VLAN reachable by both Sidero and the nodes'
  PXE NICs (Sidero's DHCP-proxy coexists with an existing DHCP server — it
  does not replace it).
- BMC/IPMI network for out-of-band power control:

  ```bash
  ipmitool -I lanplus -H <BMC_IP> -U <USER> -P <PASS> \
    chassis bootdev pxe options=persistent
  ipmitool -I lanplus -H <BMC_IP> -U <USER> -P <PASS> \
    power cycle
  ```

  Sidero can drive this itself via a `Server`'s configured BMC credentials
  for zero-touch provisioning — see `deploy/sidero/README.md`.

## Troubleshooting

- Node not PXE-booting: check BIOS/UEFI boot order, cabling/switch port, and
  that the provisioning VLAN reaches the node.
- Stuck in maintenance mode / not registering: `kubectl get servers -A` and
  check the `sidero-controller-manager` logs.
- Wrong `ServerClass` match: check the qualifiers in
  `deploy/sidero/serverclasses/` against the node's actual hardware
  (CPU/GPU/PCI addresses).
