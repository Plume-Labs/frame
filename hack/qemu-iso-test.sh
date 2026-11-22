#!/usr/bin/env bash
# Boots an image built by hack/build-iso.sh in QEMU, in BIOS or UEFI mode, and
# passes only when the installed system answers on SSH with the install UID the
# image was built with. No hardware, no input: this is the bench for the claim
# "the ISO installs a node by itself".
#
#   hack/qemu-iso-test.sh bios|uefi
#
# Needs: qemu-system-x86_64, ovmf (UEFI), xorriso, jq, ssh, a built provisiond
# (FRAME_PROVISIOND_BIN, default: go build ./cmd/provisiond). Without /dev/kvm
# QEMU emulates the CPU and an install takes an hour or more: set TIMEOUT
# (seconds, default 5400). Work files go in WORK (default ./qemu-out).
#
# The guest reaches this host at 10.0.2.2 (QEMU user networking), which is
# where provisiond serves the seed; its address is 10.0.2.15 behind the
# built-in DHCP, with SSH forwarded to 127.0.0.1:$SSH_PORT.
set -euo pipefail

mode=${1:?usage: qemu-iso-test.sh bios|uefi}
here=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-$PWD/qemu-out}; TIMEOUT=${TIMEOUT:-5400}; SSH_PORT=${SSH_PORT:-2222}
mkdir -p "$WORK/$mode"; w=$WORK/$mode
die() { echo "qemu-iso-test: $*" >&2; exit 1; }

case "$mode" in
  bios) fw=() ;;
  uefi)
    code=/usr/share/OVMF/OVMF_CODE_4M.fd; vars=/usr/share/OVMF/OVMF_VARS_4M.fd
    [ -r "$code" ] || die "OVMF not found (apt install ovmf)"
    cp "$vars" "$w/vars.fd"
    fw=(-drive "if=pflash,format=raw,readonly=on,file=$code" -drive "if=pflash,format=raw,file=$w/vars.fd") ;;
  *) die "mode must be bios or uefi" ;;
esac

bin=${FRAME_PROVISIOND_BIN:-}
if [ -z "$bin" ]; then bin=$w/provisiond; (cd "$here" && go build -o "$bin" ./cmd/provisiond); fi
[ -f "$w/key" ] || ssh-keygen -q -t ed25519 -N '' -C qemu-bench -f "$w/key"

# BASE_ISO: an already downloaded, SHA256-verified base image, to skip the 4 GB fetch.
if [ -n "${BASE_ISO:-}" ]; then mkdir -p "$w/images/base"; ln -sf "$BASE_ISO" "$w/images/base/$(basename "$BASE_ISO")"; fi
serial=frametest; size=$((8<<30))
qemu-img create -q -f qcow2 "$w/disk.qcow2" 8G

FRAME_PROVISIOND_BIN=$bin OUT=$w MEDIA_BIND=127.0.0.1 "$here/hack/build-iso.sh" \
  --hostname qemu-$mode --boot "$mode" --ssh-key "$w/key.pub" --media-ip 10.0.2.2 \
  --address 10.0.2.15/24 --gateway 10.0.2.2 --dns 10.0.2.3 \
  --disk "/dev/disk/by-id/virtio-$serial:$size"
trap '"$here/hack/build-iso.sh" --stop >/dev/null 2>&1 || true; [ -f "$w/qemu.pid" ] && kill "$(cat "$w/qemu.pid")" 2>/dev/null || true' EXIT

accel=(); [ -w /dev/kvm ] && accel=(-enable-kvm -cpu host) || accel=(-cpu max)
qemu-system-x86_64 "${accel[@]}" -m 4096 -smp 2 "${fw[@]}" \
  -drive "file=$w/disk.qcow2,if=none,id=d0,format=qcow2" -device "virtio-blk-pci,drive=d0,serial=$serial" \
  -cdrom "$w/qemu-$mode.iso" -boot order=dc \
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" -device virtio-net-pci,netdev=n0 \
  -display none -monitor "unix:$w/mon.sock,server,nowait" -pidfile "$w/qemu.pid" -daemonize

echo "==> waiting up to ${TIMEOUT}s for ssh frame@127.0.0.1:$SSH_PORT (progress: screendump via $w/mon.sock)"
end=$((SECONDS+TIMEOUT))
while [ $SECONDS -lt $end ]; do
  if got=$(ssh -i "$w/key" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o ConnectTimeout=5 frame@127.0.0.1 'cat /etc/frame-install-uid' 2>/dev/null) && [ -n "$got" ]; then
    echo "$got" >"$w/installed-uid"
    grep -q "$got" "$w"/images/*/user-data && { echo "PASS ($mode): installed system carries the image's install UID $got"; exit 0; }
    die "FAIL ($mode): the machine answers with UID $got, which this image did not carry"
  fi
  sleep 30
done
die "FAIL ($mode): no SSH after ${TIMEOUT}s"
