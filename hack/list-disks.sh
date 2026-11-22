#!/usr/bin/env bash
# Lists the disks of the machine it runs on, in the form hack/build-iso.sh asks
# for: one `--disk <by-id>:<bytes>` per whole disk. Read-only.
#
# Why it exists: the installer image names its target disk by /dev/disk/by-id
# and refuses to touch it unless the size matches, and nobody knows those names
# before the machine has booted once. Boot any Linux live system on it (the
# stock Ubuntu Server ISO is enough: Help > shell), then:
#
#   curl -fsSL <url of this file> | bash        # or copy it over with scp
#
# Prefers the wwn-/scsi-/ata-/nvme- names, which are stable; skips partitions,
# loop and optical devices, and USB sticks (the key you booted from is not a
# disk to install on).
set -euo pipefail

[ -d /dev/disk/by-id ] || { echo "list-disks: no /dev/disk/by-id here" >&2; exit 1; }

printf '%-8s %-9s %-14s %s\n' DEVICE SIZE TRANSPORT "--disk argument"
for dev in /sys/block/*; do
  name=${dev##*/}
  case "$name" in loop*|ram*|sr*|fd*|zram*|dm-*|md*) continue ;; esac
  node=/dev/$name
  [ -b "$node" ] || continue
  tran=$(lsblk -dno TRAN "$node" 2>/dev/null | tr -d ' ')
  [ "$tran" = usb ] && continue
  bytes=$(blockdev --getsize64 "$node")
  best=""
  for rank in nvme- wwn- scsi- ata-; do
    for link in /dev/disk/by-id/${rank}*; do
      [ -e "$link" ] || continue
      case "$link" in *-part*) continue ;; esac
      [ "$(readlink -f "$link")" = "$node" ] && { best=$link; break 2; }
    done
  done
  gib=$(awk -v b="$bytes" 'BEGIN{printf "%.1fG", b/1073741824}')
  if [ -n "$best" ]; then
    printf '%-8s %-9s %-14s --disk %s:%s\n' "$name" "$gib" "${tran:-?}" "$best" "$bytes"
  else
    printf '%-8s %-9s %-14s (no by-id name: cannot be named)\n' "$name" "$gib" "${tran:-?}"
  fi
done
