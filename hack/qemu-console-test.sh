#!/usr/bin/env bash
# Bench for the TEXT assistant (tty8) of the guided-installer ISO: boots the ISO
# in QEMU, TYPES the answers on the emulated keyboard (QEMU monitor `sendkey`)
# the way a person at a screen would, and passes when the assistant reports the
# installation started, and that what was typed (keyboard layout, security keys) is in the document. It stops there: the install itself is what
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
# "gb" is first: it switches the live keyboard, and every character typed afterwards (+ / = , - .) is the same
# key on a gb layout as on the US keys this script sends. Two security-key credentials, then an empty line.
cred = ["Z3JlZW5Ub21hdG9LZXlIYW5kbGU,cHVibGljS2V5QmxvYkFBQUFBQQ==,es256,+presence+pin", "YmFja3VwS2V5SGFuZGxlQkJC,YmFja3VwUHVibGljS2V5QkJCQg==,es256,+presence+pin"]
lines = ["gb", "qemu-console", "3", "1", "frametest", "static", "10.0.2.15/24", "10.0.2.2", "10.0.2.3", key] + cred + ["", "ERASE"]
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
    send("ret")
    # The first answer switches the keyboard (loadkeys): give it time to finish before typing on.
    time.sleep(8 if line == "gb" else 1.5)
PYEOF
echo "==> answers typed on tty8; waiting for the assistant to report the installation"
while [ $SECONDS -lt $end ]; do
  st=$(curl -fsS "http://127.0.0.1:$API_PORT/api/status" 2>/dev/null || true)
  case "$st" in *'"installing"'*)
      # The live system's ssh only reaches the installer's own interface, so what was typed is
      # checked on the INSTALLED system instead: wait for it (an hour or more without KVM).
      echo "==> installation started; waiting for the installed system to check what was typed"
      while [ $SECONDS -lt $end ]; do
        if got=$("${SSH[@]}" frame@127.0.0.1 'cat /etc/hostname /etc/frame-node-role /etc/default/keyboard; grep -c "^auth sufficient pam_u2f.so" /etc/pam.d/login; grep -c "^frame:" /etc/Yubico/u2f_keys' 2>/dev/null) && [ -n "$got" ]; then
          grep -qx 'qemu-console' <<<"$got" || die "FAIL ($mode): hostname typed is not the installed one: $got"
          grep -q 'XKBLAYOUT="gb"' <<<"$got" || die "FAIL ($mode): the keyboard layout typed (gb) is not installed: $got"
          [ "$(tail -2 <<<"$got" | tr '\n' ' ')" = "1 1 " ] || die "FAIL ($mode): the security-key console login typed is not installed: $got"
          echo "PASS ($mode): typed on the text console (layout gb, two security keys); the installed system has that hostname, layout and key login"; exit 0
        fi
        sleep 30
      done
      die "FAIL ($mode): the installed system never answered";; *'"failed"'*) die "FAIL ($mode): $st";; esac
  sleep 10
done
die "FAIL ($mode): the text console never started an installation (screendump: echo screendump $w/s.ppm | socat - UNIX-CONNECT:$w/mon.sock)"
