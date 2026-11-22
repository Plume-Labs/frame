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
checks that and `hack/build-iso.sh` runs it on every build.

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
curl -fsSL https://raw.githubusercontent.com/Plume-Labs/frame/main/hack/list-disks.sh | bash
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
cluster node wants a fixed address, so prefer static or a DHCP reservation.

The script prints the image path and its SHA256 and **leaves `provisiond`
running**: the machine fetches its seed from it. Keep it up until step 4.

## 3. Burn and boot

```
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
cat /etc/frame-install-uid     # the UID the build printed
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

## What is proven, and what is not

| Claim | Status |
|---|---|
| The image refuses to build without both a BIOS and a UEFI boot record | tested |
| Unattended autoinstall renders for single disk and mirror, BIOS and UEFI | tested |
| No private key or token in the image or the seed | tested on every build |
| Installs by itself in QEMU, **BIOS** and **UEFI** (`hack/qemu-iso-test.sh`) | see the pull request |
| Boots from a USB key on a real 2009-era server, with a BMC VGA console | **not tried** |
| Installs on a machine whose network has no DHCP | **not possible today**: the live installer needs one lease |

The last row is the open point: for a network without DHCP the seed would have to
travel on the key itself and the live system would need a static address on the
kernel command line, which needs the port's name. Not built; say so if you need
it.
