#!/usr/bin/env bash
# Bench for the TEXT assistant (tty8) of the guided-installer ISO: boots the ISO
# in QEMU, TYPES the answers on the emulated keyboard (QEMU monitor `sendkey`)
# the way a person at a screen would, and passes when the assistant reports the
# installation started. It stops there: the install itself is what
# qemu-assistant-test.sh proves, through the same code path.
#
#   hack/qemu-console-test.sh bios|uefi        (same needs and variables as qemu-assistant-test.sh)
set -euo pipefail
mode=${1:?usage: qemu-console-test.sh bios|uefi}
here=$(cd "$(dirname "$0")/.." && pwd)
WORK=${WORK:-$PWD/qemu-out}; TIMEOUT=${TIMEOUT:-3600}; SSH_PORT=${SSH_PORT:-2227}; API_PORT=${API_PORT:-18098}
: "${BASE_ISO:?set BASE_ISO to the Ubuntu 24.04.5 live-server ISO}"
w=$WORK/console-$mode; rm -rf "$w"; mkdir -p "$w"
die() { echo "qemu-console-test: $*" >&2; exit 1; }
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
until grep -q 'installer: open http' "$w/serial.log" 2>/dev/null; do [ $SECONDS -lt $end ] || die "FAIL ($mode): assistant never came up"; sleep 10; done
echo "==> assistant is up; giving tty8 a moment to switch"; sleep 20
user=; while [ $SECONDS -lt $end ] && [ -z "$user" ]; do
  for u in installer ubuntu-server ubuntu; do
    if "${SSH[@]}" -f -N -L "127.0.0.1:$API_PORT:127.0.0.1:8099" "$u@127.0.0.1" 2>/dev/null; then user=$u; break; fi
  done; [ -n "$user" ] || sleep 10
done
[ -n "$user" ] || die "FAIL ($mode): cannot ssh into the live system"
pgrep -f "L 127.0.0.1:$API_PORT" | head -1 >"$w/tunnel.pid"

# Type the answers. US keyboard layout in the live system.
python3 - "$w/mon.sock" "$(cat "$w/key.pub")" <<'PYEOF'
import socket, sys, time
sock, key = sys.argv[1], sys.argv[2].strip()
lines = ["qemu-console", "3", "1", "frametest", "static", "10.0.2.15/24", "10.0.2.2", "10.0.2.3", key, "ERASE"]
shifted = {'+':'equal','_':'minus','@':'2'}
plain = {' ':'spc','.':'dot','/':'slash','-':'minus','=':'equal',',':'comma','\n':'ret'}
s = socket.socket(socket.AF_UNIX); s.connect(sock); time.sleep(0.5); s.recv(65536)
def send(k):
    s.sendall(("sendkey %s\n" % k).encode()); time.sleep(0.06)
for line in lines:
    for c in line:
        if c.isupper(): send("shift-" + c.lower())
        elif c in shifted: send("shift-" + shifted[c])
        elif c in plain: send(plain[c])
        else: send(c)
    send("ret"); time.sleep(1.5)
PYEOF
echo "==> answers typed on tty8; waiting for the assistant to report the installation"
while [ $SECONDS -lt $end ]; do
  st=$(curl -fsS "http://127.0.0.1:$API_PORT/api/status" 2>/dev/null || true)
  case "$st" in *'"installing"'*) echo "PASS ($mode): typed on the text console, the assistant started the installation"; exit 0;; *'"failed"'*) die "FAIL ($mode): $st";; esac
  sleep 10
done
die "FAIL ($mode): the text console never started an installation (screendump: echo screendump $w/s.ppm | socat - UNIX-CONNECT:$w/mon.sock)"
