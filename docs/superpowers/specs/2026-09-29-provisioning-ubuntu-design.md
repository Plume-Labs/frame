# Provisioning on Ubuntu 24.04 LTS

**Decision, 2026-09-29: Frame nodes run Ubuntu 24.04 LTS, not Debian 13.** This
replaces the Debian base chosen on 2026-09-10 (`docs/provisioning.md`). Status:
draft, reviewed once against the code, the ISO and the upstream docs; awaiting
validation.

## Why

The NVIDIA GPU Operator installs the driver from a per-OS container image. On
2026-09-29 the `nvcr.io/nvidia/driver` repository carried 7,533 tags covering
Ubuntu 20.04/22.04/24.04, RHEL 8/9/10, RHCOS and Rocky — **no Debian, and no
Ubuntu 26.04**. On a Debian node the operator cannot own the driver: it has to be
installed by hand on the host (`.run` + DKMS), pinned by hand, and rebuilt by hand
when something goes wrong — outside anything Frame declares or observes.

`nvcr.io/nvidia/driver:580.178.04-ubuntu24.04` exists. 580 is the last driver
branch that supports Pascal (Tesla P4), and it is the version a GPU node on the
reference cluster already runs. On Ubuntu 24.04 the operator owns the driver as
well as the toolkit and device plugin.

24.04 rather than 26.04: 24.04 is the newest release NVIDIA publishes driver
images for, and it is LTS until 2029 (2034 with ESM).

## What changes

The installer dialect changes; the install pipeline mostly does not.

| Where | Today (Debian) | Becomes (Ubuntu) |
| --- | --- | --- |
| `preseed.go` (482) | d-i preseed rendered by `text/template`, netcfg re-run script | `autoinstall.go`: a Go struct marshalled with `yaml.Marshal` into `#cloud-config` + `autoinstall:` v1 |
| `layout.go` (112) | `PartmanRecipe`, `diskSizeAssertion` | curtin `storage:` config; the size assertion is kept as the first early-command |
| `iso.go` (237) | full extraction, isolinux + grub rewrite, `-as mkisofs` repack | native xorriso: `-indev base -outdev out -map … -boot_image any replay`, no extraction |
| `base.go` (118) | Debian netinst URL + SHA256, 4 GiB cap | `ubuntu-24.04.5-live-server-amd64.iso` + SHA256, 6 GiB cap |
| `server.go` (608) | `GET /preseed/{token}.cfg` / `.sh`, `DELETE /iso`, `LocalImageStore` | `GET /autoinstall/{token}/{file}` over a closed list; `DELETE` and `LocalImageStore.Remove` delete the new files |
| `beacon.go` | checkpoints netcfg, netcfg-done, early, partman, late; heartbeat from preseed hooks | checkpoints early, late, failed; heartbeat started from early-commands (see Beacon) |
| `cmd/bootstrap`, `LocalImageStore` | call `RenderPreseed`, write `.cfg`/`.sh` | call the autoinstall renderer, write the three files |
| `frameinstall_controller.go` | Preparing 10 min, Installing 20 min | budgets revised from the recette measurements (see Recette) |
| `Dockerfile.provisiond`, chart | "Debian installer images"; `images` emptyDir 8Gi, `/tmp` unbounded | text; `images` sized base + 3 images (≥ 16Gi); `/tmp` no longer used for extraction |
| `testdata/debian-13.6.0-boot/` | captured isolinux/grub tree | `testdata/ubuntu-24.04.5-boot/` (`grub.cfg`, `loopback.cfg`, PROVENANCE.md) |

Unchanged: Redfish/BMC control (`redfishbmc.go`), the `FrameInstall` phase
sequence, SSH marker verification and host-key capture (`ssh.go`), k3s join
(`join.go`). The CRD keeps its fields; comments naming the preseed change.

**Networking stays static-only.** `network.address` is required today
(`frameinstall_types.go`), `join.go` needs it as an IP, and `install.go` waits for
SSH on it after the disks are wiped. A DHCP-addressed node would be waited for
at an address nobody answers on. Nothing here adds a DHCP mode.

**Debian support is removed, not kept beside Ubuntu.** The one Debian node on the
reference cluster is being reinstalled on Ubuntu, and nothing needs Frame to
install Debian again; two installer dialects would double every test and every
recette for nothing.

## Facts about the 24.04.5 image (read from the ISO, 2026-09-29)

- `ubuntu-24.04.5-live-server-amd64.iso`, 4,080,486,400 bytes, SHA256
  `97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8`
  (releases.ubuntu.com `SHA256SUMS`). 95 % of today's 4 GiB cap.
- No `isolinux/`. Two El Torito entries: BIOS `boot/grub/i386-pc/eltorito.img`,
  UEFI as appended partition 2. GRUB serves both modes; kernel lines live in
  `boot/grub/grub.cfg` and `boot/grub/loopback.cfg`, as
  `linux /casper/vmlinuz  ---`. Entry 0 is the GA kernel.
- `curl`/`wget` exist in the live environment; `sudo`, `mdadm`,
  `ca-certificates`, `curl` in the target; `openssh-server`, `linux-generic`,
  `grub-efi-amd64-signed`, `efibootmgr` in the ISO pool — an offline install is
  possible.
- Volume label `Ubuntu-Server 24.04.5 LTS amd64`: kept on the remastered image
  (casper looks for its medium).

## Design

### Image

One xorriso invocation, no extraction:

```
xorriso -indev <base> -outdev <out> \
  -map <rendered grub.cfg> /boot/grub/grub.cfg \
  -map <rendered loopback.cfg> /boot/grub/loopback.cfg \
  -boot_image any replay
```

The result is refused unless `-report_el_torito` shows both the BIOS and the
UEFI entry. Today's code silently produces an unbootable image when neither
`isolinux/` nor `efi.img` is present — which is the Ubuntu layout.

### Boot arguments

Inserted before `---` on every `linux` line of both files:

```
frame=1 autoinstall ds=nocloud\;s=http://<media>/autoinstall/<token>/ ip=dhcp noprompt ---
```

- `frame=1` stays the idempotence marker `addBootArgs` keys on.
- `autoinstall` skips subiquity's confirmation prompt.
- `\;`: unescaped, GRUB ends the command at `;` and the datasource URL is lost.
  Tested on the rendered file.
- Trailing `/` on `s=`: cloud-init appends the file names to it.
- `ip=dhcp`: the autoinstall document is fetched over the network **before** its
  own `network:` section applies, so the live environment needs an address first.
  This is the same four-NICs-one-cabled problem `interface=auto` solved on Debian.
  **The install network must offer DHCP.** Whether `ip=dhcp` alone finds the
  cabled NIC on the ML350 is proven in the recette, not assumed.
- `noprompt`: the virtual medium is still inserted at reboot (ejected only at
  cleanup, `install.go`); without it casper can stop on "remove the installation
  medium, then press ENTER".
- `timeout=1`, default entry 0 (GA kernel).

Fallback if iLO 4 virtual media is too slow for 3.8 GiB: boot kernel/initrd from
the medium and add `url=http://<media>/iso/<name>` so casper loads the image over
HTTP into RAM. casper keeps the whole ISO in tmpfs on top of the live system:
**≥ 8 GiB RAM**, to measure.

### Served files

`GET /autoinstall/{token}/{file}`, `file` ∈ {`user-data`, `meta-data`,
`vendor-data`}, anything else 404, same read-only unauthenticated listener and
same token discipline as `/iso/{name}` today. `meta-data` and `vendor-data` are
empty (NoCloud supplies a default `instance-id`). `network-config` is not served:
cloud-init tolerates its 404 but retries it; the delay is measured in the recette
and the file added (empty) only if it costs.

### Autoinstall document

Built as a Go struct and marshalled — never templated — so no value can break
YAML structure. Validated at `POST` time as today, plus shape checks: hostname
RFC 1123, install UID 32 hex characters, key material refused, gateway and
nameservers as IPs. The rendered document is checked in tests against
subiquity's `autoinstall-schema.json`, pinned to the version on the 24.04.5 ISO.

```yaml
#cloud-config
autoinstall:
  version: 1
  interactive-sections: []
  refresh-installer: {update: false}
  updates: security
  apt: {fallback: offline-install}
  locale: en_US.UTF-8
  keyboard: {layout: us}
  network:
    version: 2
    ethernets:
      frame0:
        match: {name: "en*"}
        optional: true
        addresses: [<address/cidr>]
        routes: [{to: default, via: <gateway>}]
        nameservers: {addresses: [<ns>, …]}
  storage: <see Storage>
  identity: {hostname: <hostname>, username: frame, password: "<locked hash>"}
  ssh: {install-server: true, allow-pw: false, authorized-keys: [<key>]}
  early-commands:
    - <disk size assertion, exit 1 on mismatch>
    - <by-id → kernel path rewrite of /autoinstall.yaml, see Storage>
    - <beacon: early>
    - <heartbeat, backgrounded>
  late-commands:
    - curtin in-target -- sh -c 'echo "frame ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/frame && chmod 440 /etc/sudoers.d/frame'
    - curtin in-target -- sh -c 'echo <uid> > /etc/frame-install-uid && chmod 444 /etc/frame-install-uid'
    - curtin in-target -- sh -c 'echo <hostname> > /etc/hostname && grep -q "^127.0.1.1 <hostname>$" /etc/hosts || echo "127.0.1.1 <hostname>" >> /etc/hosts'
    - <beacon: late>
  error-commands:
    - <beacon: failed>
  shutdown: reboot
```

- `updates: security` is explicit. autoinstall has no "none"; the Debian preseed
  did `pkgsel/upgrade select none`. Consequence: the install reaches the Ubuntu
  archive, and the kernel installed can be newer than the ISO's. Accepted —
  security updates at first boot are wanted — and the Installing budget accounts
  for it.
- No `packages:` list: everything needed is already in the target.
- **One netplan stanza matching `en*`, with the static address, `optional:
  true`.** networkd only configures a link with carrier, so on a four-NIC machine
  with one cable the address lands on the cabled port. This is *not proven*; it
  is recette step 1. If it fails, the fallback is a `network.macAddress` field
  filled from the Redfish inventory (EthernetInterfaces with LinkUp) — a CRD
  change, deliberately not made before the measurement says it is needed. Known
  limit: two cabled ports (bonding) need that field anyway.
- **Hostname and `/etc/hosts` are written by Frame, not left to subiquity.**
  `join.go` depends on `sudo` not printing "unable to resolve host"; the Debian
  preseed wrote both files for that reason, and so does this.
- `password`: a locked hash accepted by `passwd` (the value is fixed in the
  golden test and checked in the recette); `allow-pw: false` either way.
- Every list is always rendered as a list, possibly empty (`frame bootstrap`
  renders no beacon commands): a bare comment or `- ` would be `null`, which the
  schema rejects.
- A failing early- or late-command aborts the install (subiquity documents it).
  That is what makes the size assertion a guard.

### Storage

`Layout` keeps its two kinds. The spec carries `/dev/disk/by-id/wwn-0x…` paths
(`internal/agent/disks.go`); a `wwn-` name carries no serial, and subiquity's
`serial` matches udev `ID_SERIAL`, which behind an HP P440ar in HBA mode is not
known yet. So the storage section does not match on serial.

- The rendered `storage.config` names each disk by a placeholder `path:`.
- Early-command 1 checks each by-id disk's size (`blockdev --getsize64`, today's
  `diskSizeAssertion`) and exits 1 on mismatch — before anything is probed or
  wiped.
- Early-command 2 resolves each by-id path (`readlink -f`) and rewrites the
  placeholders in `/autoinstall.yaml`, which subiquity re-reads after
  early-commands.
- `single-disk`: GPT; ESP (UEFI) or bios_grub (Legacy); `/` ext4.
- `mirror`: both disks `wipe: superblock-recursive` (the live system
  auto-assembles leftover md arrays, including Debian's); identical partitions;
  mdadm RAID1 for `/`; in UEFI an ESP on each disk with `grub_device: true` on
  the ESP partitions, in Legacy `bios_grub` on each disk with `grub_device: true`
  on the disks.
- Disks not in `storage.config` are not touched.

### Beacon

The protocol changes, and the code says so:

- Checkpoints are reduced to what is emitted: `early` (after the size assertion,
  as today), `late`, and a new `failed` from `error-commands`. `checkpointRank`
  follows. netcfg, netcfg-done and partman go.
- The heartbeat keeps the controller's 60-second "installer lost" rule true: it
  is started from the early-commands, backgrounded with
  `</dev/null >/dev/null 2>&1 &` — subiquity reads command output, and a
  foreground or attached heartbeat hangs the step, the same reason as the
  Debian hook today.

### GPU nodes (not in `charts/frame`)

The GPU Operator is a separate Helm release (`deploy/scripts/acme-bootstrap.sh`),
not part of `charts/frame`. That release changes:

- `driver.enabled=true`, `driver.nvidiaDriverCRD.enabled=true`,
  `driver.nvidiaDriverCRD.deployDefaultCR=false` (otherwise a default CR
  competes with ours).
- One `NVIDIADriver`: `driverType: gpu`, `version: 580.178.04`, `nodeSelector`
  `nvidia.com/gpu.present: "true"` **and**
  `feature.node.kubernetes.io/system-os_release.VERSION_ID: "24.04"`, so a GPU
  node still on 26.04 is not handed an image that does not exist.
- A host-installed driver is detected and labelled
  `nvidia.com/gpu.deploy.driver=pre-installed`: a mixed cluster during migration
  is safe.

## Tests

- Golden render per layout × boot mode, marshalled then validated against the
  pinned subiquity schema. (`cloud-init schema` only checks that
  `autoinstall.version` exists — not enough.)
- Refusals carried over from the preseed table, plus the new shape checks
  (hostname, UID, gateway, nameservers) and YAML-significant characters that the
  old shell-quoting guard did not cover.
- grub rewrite against `testdata/ubuntu-24.04.5-boot/`: arguments on every
  `linux` line of both files, before `---`, `\;` escaped, idempotent via
  `frame=1`.
- Remaster: output keeps both El Torito entries and the volume label; an output
  missing either is refused (test with a base lacking the UEFI entry).
- Served files: the three names answer, any other name 404s, `DELETE` removes
  them.
- Beacon: the rendered heartbeat line is backgrounded with all three streams
  redirected; `failed` ranks as terminal.
- Mutation check on each guard, one guard at a time.

## Recette on hardware

HPE ML350 Gen9 (iLO 4, P440ar in HBA mode, two SATA SSD mirror, four NICs, one
cabled). Each step is a measurement written down, not a pass/fail only:

1. UEFI, `mirror`, static address: install completes unattended; the address is
   on the cabled port (`en*` + carrier); `ip=dhcp` found that port in the live
   environment; `sudo -n true` prints nothing on stderr; marker read over SSH;
   node joins k3s.
2. Times: base download, remaster, virtual-media read of the squashfs,
   `updates: security`, `network-config` retry delay. Phase budgets set from
   these, with margin, in the same delivery. Decide the `url=` fallback here.
3. `udevadm info` on each disk (recorded for a future serial match).
4. Two reboots: `/` on md0 [UU] both times; boot from each ESP by swapping the
   boot order (nothing pulled — no hot removal on this controller).
5. Reboot with the medium still attached: no prompt (`noprompt`).
6. GPU node: `NVIDIADriver` rolls out, driver pod `nvidia-smi` sees the card,
   validator green, a CUDA pod runs.

## Out of scope

- Upgrading existing Ubuntu 26.04 nodes: not installed by Frame, being replaced
  by new hardware.
- Unattended OS upgrades policy on Frame nodes.
- A DHCP addressing mode.
