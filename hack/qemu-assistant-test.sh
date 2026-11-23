#!/usr/bin/env bash
# Bench for the guided installer: builds the installer ISO (build-installer-iso.sh,
# test flags), boots it in QEMU in BIOS or UEFI mode, reads the assistant's console
# token from the serial log, drives the assistant's HTTP API through an SSH port
# forward (the way a person drives its page), and passes only when the installed
# system answers on SSH with the install UID the assistant returned.
#
#   hack/qemu-assistant-test.sh bios|uefi
#
# Needs: qemu-system-x86_64, ovmf (UEFI), xorriso, jq, curl, ssh, and BASE_ISO
# (the Ubuntu 24.04.5 live-server image). Without /dev/kvm an install takes an hour
# or more: TIMEOUT (seconds, default 5400). Work files: WORK (default ./qemu-out).
# This tests the assistant and the injection, NOT the page in a browser and NOT a
# kiosk session.
set -euo pipefail
mode=${1:?usage: qemu-assistant-test.sh bios|uefi}
here=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-$PWD/qemu-out}; TIMEOUT=${TIMEOUT:-5400}; SSH_PORT=${SSH_PORT:-2226}; API_PORT=${API_PORT:-18099}
: "${BASE_ISO:?set BASE_ISO to the Ubuntu 24.04.5 live-server ISO}"
w=$WORK/assistant-$mode; rm -rf "$w"; mkdir -p "$w"
die() { echo "qemu-assistant-test: $*" >&2; exit 1; }
case "$mode" in
  bios) fw=() ;;
  uefi) code=/usr/share/OVMF/OVMF_CODE_4M.fd; cp /usr/share/OVMF/OVMF_VARS_4M.fd "$w/vars.fd"
        fw=(-drive "if=pflash,format=raw,readonly=on,file=$code" -drive "if=pflash,format=raw,file=$w/vars.fd") ;;
  *) die "mode must be bios or uefi" ;;
esac
ssh-keygen -q -t ed25519 -N '' -C qemu-bench -f "$w/key"
cleanup() { [ -f "$w/qemu.pid" ] && kill "$(cat "$w/qemu.pid")" 2>/dev/null || true; [ -f "$w/tunnel.pid" ] && kill "$(cat "$w/tunnel.pid")" 2>/dev/null || true; }
trap cleanup EXIT
"$here/hack/build-installer-iso.sh" --base "$BASE_ISO" --out "$w/installer.iso" --test-ssh-key "$w/key.pub" --serial-console >/dev/null

qemu-img create -q -f qcow2 "$w/disk.qcow2" 8G
accel=(-cpu max); [ -w /dev/kvm ] && accel=(-enable-kvm -cpu host)
qemu-system-x86_64 "${accel[@]}" -m 4096 -smp 2 "${fw[@]}" \
  -drive "file=$w/disk.qcow2,if=none,id=d0,format=qcow2" -device virtio-blk-pci,drive=d0,serial=frametest \
  -cdrom "$w/installer.iso" -boot order=c,once=d \
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" -device virtio-net-pci,netdev=n0 \
  -display none -serial "file:$w/serial.log" -monitor "unix:$w/mon.sock,server,nowait" -pidfile "$w/qemu.pid" -daemonize

SSH=(ssh -i "$w/key" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 -o LogLevel=ERROR)
end=$((SECONDS+TIMEOUT))
echo "==> waiting for the assistant's console line"
token=
while [ $SECONDS -lt $end ]; do
  token=$(tr -d '\r' <"$w/serial.log" 2>/dev/null | sed -n 's|.*installer: open http://127.0.0.1:[0-9]*/#\([0-9a-f]\{32\}\).*|\1|p' | tail -1)
  [ -n "$token" ] && break; sleep 10
done
[ -n "$token" ] || die "FAIL ($mode): no assistant console line after ${TIMEOUT}s (see $w/serial.log)"
echo "==> assistant is up (token read from the console, not from the image)"

echo "==> opening the forward to the assistant, which listens on the live system's loopback only"
user=
while [ $SECONDS -lt $end ] && [ -z "$user" ]; do
  for u in installer ubuntu-server ubuntu; do
    if "${SSH[@]}" -f -N -L "127.0.0.1:$API_PORT:127.0.0.1:8099" "$u@127.0.0.1" 2>/dev/null; then user=$u; break; fi
  done
  [ -n "$user" ] || sleep 10
done
[ -n "$user" ] || die "FAIL ($mode): cannot ssh into the live system"
pgrep -f "L 127.0.0.1:$API_PORT" | head -1 >"$w/tunnel.pid"

api() { curl -fsS -H 'Content-Type: application/json' -H "X-Installer-Token: $token" "$@"; }
hw=$(api "http://127.0.0.1:$API_PORT/api/hardware") || die "FAIL ($mode): hardware endpoint"
byid=$(jq -r '.hardware.disks[] | select(.serial=="frametest" or (.byID|test("frametest"))) | .byID' <<<"$hw" | head -1)
[ -n "$byid" ] || die "FAIL ($mode): the test disk is not offered: $(jq -c .hardware.disks <<<"$hw")"
want=$([ "$mode" = uefi ] && echo true || echo false); [ "$(jq .hardware.uefi <<<"$hw")" = "$want" ] || die "FAIL ($mode): hardware.uefi is $(jq .hardware.uefi <<<"$hw")"
conf=$(jq -r --arg b "$byid" '.hardware.disks[] | select(.byID==$b) | (if .serial=="" then (.byID|split("/")|last) else .serial end)' <<<"$hw")

body=$(jq -n --arg b "$byid" --arg c "$conf" --arg k "$(cat "$w/key.pub")" \
  '{hostname:"qemu-assistant",role:"worker",disks:[{byID:$b,confirm:$c}],address:"10.0.2.15/24",gateway:"10.0.2.2",dns:["10.0.2.3"],sshPublicKey:$k,u2fKeys:[]}')
# A wrong serial must be refused before anything is written.
if api -X POST -d "$(jq '.disks[0].confirm="nope"' <<<"$body")" "http://127.0.0.1:$API_PORT/api/install" >/dev/null 2>&1; then die "FAIL ($mode): a wrong serial was accepted"; fi
uid=$(api -X POST -d "$body" "http://127.0.0.1:$API_PORT/api/install" | jq -r .uid) || die "FAIL ($mode): install refused"
[ -n "$uid" ] && [ "$uid" != null ] || die "FAIL ($mode): no uid returned"
echo "==> installation started, uid $uid; waiting for ssh frame@127.0.0.1:$SSH_PORT"
while [ $SECONDS -lt $end ]; do
  if got=$("${SSH[@]}" frame@127.0.0.1 'cat /etc/frame-install-uid; cat /etc/frame-node-role' 2>/dev/null) && [ -n "$got" ]; then
    [ "$(sed -n 1p <<<"$got")" = "$uid" ] || die "FAIL ($mode): UID $(sed -n 1p <<<"$got") is not the one returned ($uid)"
    [ "$(sed -n 2p <<<"$got")" = worker ] || die "FAIL ($mode): role not recorded"
    echo "PASS ($mode): the assistant drove an unattended install; the installed system carries UID $uid and role worker"
    exit 0
  fi
  sleep 30
done
die "FAIL ($mode): no installed system after ${TIMEOUT}s"
