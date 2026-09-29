# Provisioning on Ubuntu 24.04 LTS

**Decision, 2026-09-29: Frame nodes run Ubuntu 24.04 LTS, not Debian 13.** This
replaces the Debian base chosen on 2026-09-10 (`docs/provisioning.md`). Status:
draft, awaiting validation.

## Why

The NVIDIA GPU Operator installs the driver from a per-OS container image. On
2026-09-29 the `nvcr.io/nvidia/driver` repository carried 7,533 tags covering
Ubuntu 20.04/22.04/24.04, RHEL 8/9/10, RHCOS and Rocky — **no Debian, and no
Ubuntu 26.04**. On a Debian node the operator cannot own the driver: it has to be
installed by hand on the host (`.run` + DKMS), pinned by hand, and rebuilt by hand
when something goes wrong — outside anything Frame declares or observes.

`nvcr.io/nvidia/driver:580.178.04-ubuntu24.04` exists. 580 is the last driver
branch that supports Pascal (Tesla P4), and it is the version a GPU node on the
reference cluster already runs. On Ubuntu 24.04 the operator owns the whole GPU
stack: driver, toolkit, device plugin, DCGM.

24.04 rather than 26.04: 24.04 is the newest release NVIDIA publishes driver
images for, and it is LTS until 2029 (2034 with ESM).

## What changes, measured

Of `internal/provision` (7,595 lines with tests), the Debian-specific part is the
installer dialect, not the install pipeline:

| File | Debian-specific | Becomes |
| --- | --- | --- |
| `preseed.go` (482) | d-i preseed, netcfg re-run script, partman early_command | `autoinstall.go`: cloud-init `#cloud-config` with `autoinstall:` v1 |
| `layout.go` (112) | `PartmanRecipe` | curtin `storage:` config (disks by serial / by-id, mdadm RAID1 for `mirror`) |
| `iso.go` (237) | isolinux + grub cfg rewrite, isolinux El Torito repack | grub-only rewrite; repack with `-boot_image any replay` |
| `base.go` (118) | pinned Debian netinst URL + SHA256 | pinned `ubuntu-24.04.5-live-server-amd64.iso` + SHA256 |
| `server.go` (608) | `GET /preseed/{token}.cfg` and `.sh` | `GET /autoinstall/{token}/user-data`, `/meta-data`, `/vendor-data` |
| `Dockerfile.provisiond`, chart | "Debian installer images", emptyDir sized for netinst | text + emptyDir sized for a 3.8 GiB base |
| `testdata/debian-13.6.0-boot/` | captured isolinux/grub tree | `testdata/ubuntu-24.04.5-boot/` with PROVENANCE.md |

Unchanged: Redfish/BMC control (`redfishbmc.go`), install phases and their
timeouts (`install.go`), the beacon protocol (`beacon.go`, `beaconstore.go`),
SSH verification of the marker (`ssh.go`), k3s join (`join.go`), the
`FrameInstall` phases. The CRD keeps its shape; only comments naming the preseed
change, plus the `layout` validation messages.

**Debian support is removed, not kept beside Ubuntu.** The one Debian node on
the reference cluster is being reinstalled on Ubuntu, and nothing needs Frame to
install Debian again; two
installer dialects would double every test and every recette for nothing.

## Facts about the 24.04.5 image (read from the ISO, 2026-09-29)

- `ubuntu-24.04.5-live-server-amd64.iso`, 4,080,486,400 bytes, SHA256
  `97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8` (releases.ubuntu.com `SHA256SUMS`).
- No `isolinux/`. Two El Torito images: BIOS (`boot/grub/i386-pc/eltorito.img`)
  and UEFI (appended partition). GRUB serves both modes from one
  `boot/grub/grub.cfg`; `boot/grub/loopback.cfg` also carries kernel lines.
- Menu entry kernel line: `linux /casper/vmlinuz ---`. Kernel arguments go
  **before** `---`.
- 4,080,486,400 bytes is 95 % of `maxBaseImageBytes` (4 GiB). Raise the cap to
  6 GiB so the next point release is not refused.

## Design

### Boot arguments

Rewritten into every `linux` line of `grub.cfg` and `loopback.cfg`:

```
autoinstall ds=nocloud\;s=http://<media>/autoinstall/<token>/ ---
```

- `autoinstall` on the kernel command line skips subiquity's confirmation
  prompt; without it an unattended install stops and waits.
- The `;` must be escaped in grub.cfg, otherwise GRUB ends the command there and
  the datasource URL is lost. Test on the rendered file, not on the template.
- The trailing `/` on `s=` is required: cloud-init appends `user-data` to it.
- `timeout=30` becomes `timeout=1`, default entry 0 (GA kernel, not HWE: the
  operator's driver image builds against the running kernel's headers either way,
  and GA changes less often).

Open question, measured on hardware only: whether iLO 4 virtual media reads a
3.8 GiB image in acceptable time. The Debian netinst was 0.79 GB. Fallback, if it
is too slow: keep the ISO for the kernel/initrd only and add
`url=http://<media>/iso/<name>` so casper fetches the image over HTTP into RAM
(needs RAM > 4 GiB, true of every node class Frame targets).

### Autoinstall document

Served per token at `/autoinstall/<token>/user-data` (plus an empty
`meta-data` and `vendor-data`: cloud-init fetches all three and a 404 on any of
them aborts the datasource). Same guarantees as the preseed today: rendered and
refused at `POST` time, no private key material, every interpolated value
validated before templating.

```yaml
#cloud-config
autoinstall:
  version: 1
  interactive-sections: []
  refresh-installer: { update: false }
  locale: en_US.UTF-8
  keyboard: { layout: us }
  network:            # netplan v2: static if the spec sets an address, DHCP on every NIC otherwise
  storage:            # from Layout, see below
  identity: { hostname: <hostname>, username: frame, password: "!" }   # locked password
  ssh: { install-server: true, allow-pw: false, authorized-keys: [<key>] }
  packages: [curl, ca-certificates]
  early-commands:     # beacon: CheckpointEarly
  late-commands:
    - curtin in-target -- sh -c 'echo "frame ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/frame && chmod 440 /etc/sudoers.d/frame'
    - curtin in-target -- sh -c 'echo <uid> > /etc/frame-install-uid && chmod 444 /etc/frame-install-uid'
    - # beacon: CheckpointLateDone
  shutdown: reboot
```

- The Debian netcfg re-run (`RenderRunScript`) disappears: subiquity applies the
  `network:` section itself, before the install, so the ordering problem it
  solved does not exist here.
- "Four NICs, one cabled" (`interface=auto` on Debian): netplan with DHCP on
  every ethernet (`match: {name: "en*"}`, `optional: true`) comes up on the
  cabled one without naming it. With a static spec the interface is matched by
  MAC, never by kernel name.
- The marker, the `frame` user, the sudoers file and the hostname are the same
  four facts `ssh.go` verifies today. The contract with the controller does not
  change.

### Storage

`Layout` keeps its two kinds.

- `single-disk`: one disk, GPT, ESP (UEFI) or bios_grub (Legacy), `/` ext4 on
  the rest.
- `mirror`: two disks, identical partitioning, mdadm RAID1 for `/`, ESP on each
  disk (UEFI) or bios_grub on each (Legacy), `grub_device: true` on both.

Disks are matched by `serial` (curtin `match: {serial: …}`), derived from the
by-id path the spec already carries. A by-id name that yields no serial is
refused at `POST`, like today's malformed paths. Any disk not named is not
touched: curtin only wipes disks referenced in `storage.config`.

### Beacon

Checkpoints move from d-i hooks to autoinstall hooks: `early-commands` (network
up, installer started), `late-commands` (target written), then the existing
SSH-side checks after reboot. `curl` is present in the live environment. The
beacon store and its protocol do not change.

### GPU nodes (outside `internal/provision`)

Not part of the installer. The chart gains an optional `NVIDIADriver` resource
(`driverType: gpu`, `version: 580.178.04`, `nodeSelector:
nvidia.com/gpu.present: "true"`) and the `ClusterPolicy` switches to
`driver.useNvidiaDriverCRD: true`. A node with a host-installed driver is
detected by the operator and labelled `nvidia.com/gpu.deploy.driver=pre-installed`,
so a mixed cluster during migration is safe.

## Tests

- Golden render of the autoinstall document per layout × boot mode × network
  mode; parsed back as YAML and checked with `cloud-init schema` (Ubuntu
  container in CI) — a template that renders but does not parse is the failure
  this catches.
- grub rewrite against the captured `testdata/ubuntu-24.04.5-boot/` tree: every
  `linux` line carries the arguments, before `---`, with `\;` escaped;
  `loopback.cfg` included.
- Repack: rebuilt image keeps both El Torito entries (`xorriso
  -report_el_torito`), BIOS and UEFI.
- Refusals: a by-id path without a serial, key material, shell metacharacters —
  same table as the preseed tests, carried over.
- Mutation check on each guard (one guard at a time).

## Recette on hardware

On the reference HPE ML350 Gen9 (iLO 4, P440ar in HBA mode, 2 SATA SSD mirror):

1. UEFI boot, `mirror` layout, DHCP: install completes unattended, `md0` [UU],
   marker readable over SSH, node joins k3s.
2. Time the virtual-media read of the 3.8 GiB image; decide on the `url=`
   fallback from that number.
3. Reboot twice: `/` on md0 both times, both ESPs bootable (pull nothing — boot
   order swap only).
4. GPU node: `NVIDIADriver` 580 rolls out, `nvidia-smi` in the driver pod,
   validator green, a CUDA pod runs.

## Out of scope

- Upgrading existing Ubuntu 26.04 nodes: they were not installed by Frame and
  are being replaced by new hardware.
- Automatic OS upgrades on Frame nodes (unattended-upgrades policy): separate
  decision.
