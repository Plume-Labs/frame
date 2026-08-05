# Omni — bare-metal provisioning for Frame

Replaces `deploy/sidero/`. Sidero Labs states in the Sidero Metal README that it
is **no longer actively developing Sidero Metal**, and points at Omni plus the
bare-metal infrastructure provider instead. Building further on Sidero would be
investing in a project upstream has stopped.

## Status: prepared, not deployed

Nothing here is applied to the test cluster, deliberately:

- **Omni manages Talos machines.** The test cluster is k3s on Ubuntu 26.04, so
  Omni has nothing to manage there — it would be an empty console.
- **There is no bare metal yet.** The cluster is three Proxmox VMs. The
  bare-metal provider PXE-boots and power-cycles physical machines; with none
  present it has nothing to provision.

So this is IaC held ready rather than a running service. When hardware arrives,
`omni-up.sh` is the entry point.

## What is here

| Path | Purpose |
| --- | --- |
| `values.yaml` | Helm values for self-hosted Omni (Dex local auth, Traefik ingress, cert-manager TLS) |
| `machineclasses/` | The hardware profiles, translated from the Sidero ServerClasses |
| `../scripts/omni-up.sh` | Generates the secrets, installs the chart, prints the follow-up steps |

## Requirements before this can run

- **Three DNS names** resolving to the cluster ingress: `omni.<domain>`,
  `kubernetes.<domain>`, `siderolink.<domain>`.
- **A reachable UDP endpoint** for WireGuard (SideroLink), NodePort `30180` by
  default. This is the one that bites: SideroLink is WireGuard, so any NAT or
  firewall between Omni and the machines has to pass UDP.
- **cert-manager with a ClusterIssuer** — the cluster has cert-manager v1.21
  installed but no issuer wired up yet.
- **BMC on every machine** (IPMI or Redfish). The bare-metal provider power-
  cycles hardware to provision it; a machine without out-of-band management
  cannot be enrolled.
- **A host in the machines' subnet** to run the provider container.

## Licence

Omni is BSL-1.1: self-hosting is free for **non-production** use. A production
deployment needs a licence from Sidero. Worth settling before this becomes load-
bearing rather than after.

## What changed from Sidero, semantically

Sidero `ServerClass` matched **discovered hardware** — the GPU class keyed off
PCI vendor `10de:`, the RDMA class off Mellanox `15b3:`. Machines classified
themselves on first boot.

Omni has no equivalent automatic PCI label. Its automatic labels cover
architecture, CPU count and similar; a role like "this is a GPU worker" is
stamped as an **initial label at boot-media generation** (`omnictl download
--initial-labels role=gpu-worker`) or set by hand afterwards.

The practical consequence: **you generate one boot image per hardware profile**
instead of one image that sorts machines automatically. `machineclasses/`
matches on those roles. Do not read the translated classes as if the old
auto-discovery survived — it did not.
