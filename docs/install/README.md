# Installing a Hearthrack node from a USB key

For a machine with no virtual media (older BMCs, no BMC): you build one
unattended Ubuntu Server 24.04 installer image for that machine, burn it on a USB
key, boot from it, and the machine installs itself and comes up on SSH. Nothing
is typed on the machine. This is the same installer `frame bootstrap` and the
`FrameInstall` controller use (`internal/provision`); only the delivery differs.

What the image contains: the stock Ubuntu live-server ISO, byte for byte, plus
two edited GRUB files that tell the installer to run unattended and where to
fetch its seed. The seed (disk, network, hostname, your **public** SSH key) is
served by `frame-provisiond` while the machine installs. **No private key and no
cluster token is ever written to the image or the seed**; `hack/check-iso-secrets.sh`
checks the two GRUB files and the seed (including its base64 payloads) and `hack/build-iso.sh` runs it on every build.

## What you need

- A Linux host on the machine's network with `podman` (or a `provisiond` binary,
  see `FRAME_PROVISIOND_BIN`), `curl`, `jq`, `xorriso`, and 6 GB free. Its address
  on that network is `--media-ip`.
- **DHCP on the machine's network while it installs.** The live installer fetches
  its seed before its own network section applies, so it asks DHCP once, whatever
  address the installed system ends up with (see the open point below).
- A USB key of 8 GB or more, and a monitor or console to choose the boot device
  the first time.

## 1. Find the disk (once per machine)

The image refuses to wipe anything it was not told to, by name **and** size. So
you need the disk's stable name before you build. Boot the machine from any
Ubuntu Server live USB, open a shell (Help > Enter shell), and run:

```
# read it first, then run as root; pin a tag or commit instead of main for anything serious
curl -fsSL https://raw.githubusercontent.com/Plume-Labs/frame/main/hack/list-disks.sh | sudo bash
```

(or copy `hack/list-disks.sh` over with a second key). It prints one line per
disk, ready to paste:

```
DEVICE   SIZE      TRANSPORT      --disk argument
sda      279.4G    sas            --disk /dev/disk/by-id/scsi-3600…:300000000000
```

Note the ones you want to install on. USB sticks are skipped. Two `--disk`
arguments make a RAID 1 mirror. Also note whether the machine boots **BIOS**
(older machines: look for "Legacy" in its setup) or **UEFI**: the partition
layout differs, so you must say.

## 2. Build the image

```
hack/build-iso.sh --hostname tacos --boot bios \
    --ssh-key ~/.ssh/id_ed25519.pub --media-ip 192.168.2.210 \
    --disk /dev/disk/by-id/scsi-3600…:300000000000 \
    --dhcp                      # or: --address 192.168.2.202/24 --gateway 192.168.2.254 --dns 192.168.2.254
```

`--dhcp` or a static address only chooses the **installed** system's network. A
static address is pinned by MAC to the port that reached the gateway during the
install, so a second cable later does not take the same address. DHCP keeps the
installer's own stanza (DHCP on every cabled port): fine for a trial, but a
cluster node wants a fixed address, so prefer static or a DHCP reservation. With several cables, DHCP
may hand the node one lease and one default route per cabled port; a static address
is pinned to a single port. In DHCP mode read the lease on the router or the console.

The script prints the image path and its SHA256 and **leaves `provisiond`
running**: the machine fetches its seed from it. Keep it up until step 4.

## 3. Burn and boot

```
# check the device with lsblk first: dd overwrites it without asking
sudo dd if=iso-out/tacos.iso of=/dev/sdX bs=4M status=progress conv=fsync
```

Boot the machine from the key. It installs, reboots into the new system and
disappears from the installer. The image has **no timeout menu to answer**: the
whole thing is unattended.

**Remove the key at the first reboot** if the machine boots the key before its
disk, or it will install again (the second run wipes the same disk again, and
loops). If the machine's boot order cannot be changed, pull the key when the
screen shows the first reboot.

## 4. Check, then stop provisiond

```
ssh frame@<address>            # key only; the account has no password
cat /etc/frame-install-uid     # must equal the "install UID" the build printed
hack/build-iso.sh --stop
```

`frame` has passwordless `sudo`. The node is a plain Ubuntu 24.04 host: it is
**not** in any cluster yet.

## 5. Join it to a cluster (a separate, deliberate step)

Nothing in the image joins a cluster, by design: the join token would have to be
on the key. Join by hand, typing the token yourself and never saving it in a file
that is committed or copied:

- As a k3s agent (worker, Ceph host): the `k3s agent` install line from the k3s
  documentation, with `K3S_URL=https://<server>:6443` and `K3S_TOKEN` read from
  the server (`sudo cat /var/lib/rancher/k3s/server/node-token`). Pin the same
  `K3S_VERSION` as the cluster. `internal/provision/join.go` shows the checks the
  controller makes after a join, and what labels and taints it expects.
- As a control-plane server with etcd: follow the cluster's own runbook; it also
  changes the etcd membership and is not a step to take from this page.

## Optional: YubiKey login on the local console

By default the `frame` account has no password, so the machine's own keyboard and
screen cannot log in at all: SSH (your key) is the only way in. If you want to log in at
the console too, without a password, enrol a FIDO2 key (a YubiKey) **before building**:

```
ykman fido access change-pin                 # once per key: the console asks key + PIN
hack/enroll-yubikey.sh -o u2f-keys.txt 2     # touch each key; the second is a spare: use a DIFFERENT physical key each round, the script cannot tell
hack/build-iso.sh ... --u2f-keys u2f-keys.txt
```

What the installed machine gets: `libpam-u2f` (installed from the Ubuntu archive during
the install, so the machine needs internet then; if it has none the install fails rather than
finishing without the login), your credentials in `/etc/Yubico/u2f_keys`, and one line in
`/etc/pam.d/login`. **Only the console login changes**: SSH stays key-only, sudo as before.

- The file holds a key handle and a public key per key: nothing that opens a door without the
  physical key and its PIN. Keep it, but it is not a secret in the way a private key is.
- The PIN is mandatory (`pinverification=1`): a key found on a desk does not open a console.
- **Break-glass**: your SSH key, and the spare key. There is deliberately no password fallback.
- Credentials are bound to the fixed origin `pam://hearthrack`, so one key works on every node.
- Up to four keys.

Proven in QEMU: the package, the file, the PAM line, sshd and sudo untouched
(`U2F=1 hack/qemu-iso-test.sh uefi`). **Not proven: a real login with a real key**: that needs a
physical YubiKey on a real console. Try it on one node, keeping an SSH session open, before
relying on it.

## What is proven, and what is not

| Claim | Status |
|---|---|
| The image refuses to build without both a BIOS and a UEFI boot record | tested |
| Unattended autoinstall renders for single disk and mirror, BIOS and UEFI | tested |
| No private key or token in the image or the seed | tested on every build |
| Installs by itself in QEMU, **BIOS** and **UEFI** (`hack/qemu-iso-test.sh`, static address; `DHCP=1` for the DHCP mode) | run by hand, no CI job (needs ~30 min per mode without KVM) |
| Boots from a USB key on a real 2009-era server, with a BMC VGA console | **not tried** |
| Installs on a machine whose network has no DHCP | **not possible today**: the live installer needs one lease |

The last row is the open point: for a network without DHCP the seed would have to
travel on the key itself and the live system would need a static address on the
kernel command line, which needs the port's name. Not built; say so if you need
it.

## Guided installer (in progress)

`cmd/installer` is the first stage of a guided installation: a local page that
replaces the hand-written spec of `hack/build-iso.sh`. It is **not on the ISO yet**
(the kiosk session and the BIOS/UEFI boot tests are the next stage) and has never
run on real hardware. What exists and is tested:

- hardware detection (disks only by `by-id`, USB disks refused, serial retyped before
  any erase), the same spec validation as the unattended image, loopback-only listener
  with Host and Origin checks, and a single installation per run;
- it writes `/autoinstall.yaml` and restarts `snap.subiquity.subiquity-server`, the
  injection proven in spike S0 (UEFI, QEMU).

Not covered yet: the k3s join token (the join stays the separate step above, so the
token is never typed into the live system), live progress, BIOS injection, the VGA
rendering of a BMC. Dry run on any Linux machine, nothing is written to a disk:

    go run ./cmd/installer --autoinstall /tmp/autoinstall.yaml --restart-subiquity=false
