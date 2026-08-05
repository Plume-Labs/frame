# PXE Boot

Frame nodes PXE-boot into **Talos Linux**, an immutable, API-managed OS with
no package manager, shell, or SSH. There is no Kickstart/preseed/kernel-image
step to configure here — that flow applies to traditional distros (Ubuntu,
Rocky, Debian), not Talos.

## What serves PXE boot

**Omni's bare-metal infrastructure provider** (`deploy/omni/`). This replaced
Sidero Metal, which used to fill this role: Sidero Labs no longer develops
Sidero Metal and points at Omni instead.

The provider runs as a container **in the machines' own subnet** — not
in-cluster, which is where Sidero ran. It:

1. Serves PXE/iPXE to bare-metal machines on that subnet.
2. Boots them into a Talos image from the Image Factory, carrying SideroLink
   kernel args so the machine dials home to Omni.
3. Registers each machine in Omni, where a `MachineClass`
   (`deploy/omni/machineclasses/`) matches it into a hardware profile.
4. Installs Talos to disk and joins the cluster from a cluster template, with
   the node labels in `deploy/omni/patches/`.

Two differences from the Sidero flow, both of which change what you have to do:

- **Power control is mandatory, not optional.** The provider drives machines
  through IPMI or Redfish. A machine with no working BMC cannot be enrolled at
  all, where Sidero could still adopt a manually-booted server.
- **Hardware no longer classifies itself.** Sidero matched PCI vendor IDs, so a
  GPU box sorted itself into the GPU class on first boot. Omni has no automatic
  PCI label: the role is stamped into the boot media (`omnictl download iso
  --initial-labels frame-role=gpu-worker`), so you produce one image per profile
  and boot the right machine from the right one.

See `deploy/omni/README.md` for the full flow and `deploy/talos/README.md` for
the MachineConfig side.

## Status

Nothing here is deployed. The test cluster is k3s on Ubuntu, so Omni — which
manages Talos machines — has nothing to manage there, and there is no bare
metal to provision. This is the documented path for when hardware arrives.

## Network requirements

- A provisioning network/VLAN reachable by both the provider host and the
  nodes' PXE NICs.
- **UDP** to the Omni SideroLink endpoint (WireGuard, NodePort 30180 by
  default) from every machine. A firewall or NAT that drops UDP produces
  machines that enrol and then never connect — which reads as a broken machine
  rather than a blocked port.
- BMC/IPMI network for out-of-band power control:

  ```bash
  ipmitool -I lanplus -H <BMC_IP> -U <USER> -P <PASS> \
    chassis bootdev pxe options=persistent
  ipmitool -I lanplus -H <BMC_IP> -U <USER> -P <PASS> \
    power cycle
  ```

  The bare-metal provider drives this itself once the BMC credentials are
  registered, for zero-touch provisioning.

## Troubleshooting

- Node not PXE-booting: check BIOS/UEFI boot order, cabling/switch port, and
  that the provisioning VLAN reaches both the node and the provider host.
- Machine appears in Omni but never becomes ready: almost always SideroLink
  UDP blocked between the machine and the WireGuard endpoint.
- Machine lands in the wrong class: check which boot image it used. The role
  comes from the image's initial labels, not from its hardware, so a GPU box
  booted from the base image joins as a base worker and looks perfectly healthy
  while doing it.
