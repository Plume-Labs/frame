#!/usr/bin/env bash
# Builds the guided-installer ISO: the Ubuntu live-server ISO, unchanged except
# for three additions, with no extraction of the 3-4 GB tree:
#   /frame/installer       the assistant (cmd/installer): page on loopback + text mode on tty8
#   /server/user-data,meta-data   a cloud-init seed read from the ISO itself
#                                 (ds=nocloud;s=/cdrom/server/) that starts it
#   the GRUB kernel lines  `autoinstall ds=nocloud\;s=/cdrom/server/`
# Without an autoinstall document the installer waits; the assistant writes one
# (spike S0) from the person's answers. No network seed, no provisiond, and
# NOTHING secret on the medium: the assistant makes a one-time token at boot and
# prints it on the console.
#
#   hack/build-installer-iso.sh --base ubuntu-24.04.5-live-server-amd64.iso --out hearthrack-installer.iso
#       [--installer BIN]       default: go build ./cmd/installer
#       [--test-ssh-key PUB]    TEST BUILDS ONLY: lets the QEMU bench reach the live system
#       [--serial-console]      TEST BUILDS ONLY: also log to ttyS0, so the bench can read the token
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
base=; out=; bin=; sshkey=; serial=0
while [ $# -gt 0 ]; do
  case $1 in
    --base) base=$2; shift 2;; --out) out=$2; shift 2;; --installer) bin=$2; shift 2;;
    --test-ssh-key) sshkey=$2; shift 2;; --serial-console) serial=1; shift;;
    *) echo "unknown argument $1" >&2; exit 2;;
  esac
done
[ -f "$base" ] && [ -n "$out" ] || { sed -n 2,20p "$0" >&2; exit 2; }
for t in xorriso sed; do command -v $t >/dev/null || { echo "$t is required" >&2; exit 1; }; done
w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
if [ -z "$bin" ]; then bin=$w/installer; (cd "$here" && CGO_ENABLED=0 go build -o "$bin" ./cmd/installer); fi

# GRUB: add the arguments to every kernel line (the ISO's own, before ---).
args='autoinstall ds=nocloud\\;s=/cdrom/server/'; # two backslashes: sed turns them into the one GRUB needs
[ $serial = 1 ] && args="$args console=tty0 console=ttyS0,115200"
maps=()
for f in /boot/grub/grub.cfg /boot/grub/loopback.cfg; do
  l=$w/$(basename "$f")
  xorriso -osirrox on -indev "$base" -extract "$f" "$l" >/dev/null 2>&1 || { [ "$f" = /boot/grub/grub.cfg ] && { echo "no $f in $base" >&2; exit 1; }; continue; }
  chmod 644 "$l"
  grep -qE '^\s*linux\s+/casper/' "$l" || { echo "$f has no casper kernel line: not a live-server ISO" >&2; exit 1; }
  sed -i -E "/^\s*linux\s+\/casper\//s# +---\s*\$# $args ---#" "$l"
  grep -q 'ds=nocloud' "$l" || { echo "$f: could not add the arguments (no trailing --- on the kernel line)" >&2; exit 1; }
  maps+=(-map "$l" "$f")
done

mkdir -p "$w/server"
printf 'instance-id: hearthrack-installer\n' >"$w/server/meta-data"
{
  echo '#cloud-config'
  if [ -n "$sshkey" ]; then printf 'ssh_authorized_keys:\n  - %s\n' "$(cat "$sshkey")"; fi
  cat <<'YAML'
write_files:
  - path: /etc/systemd/system/frame-installer.service
    permissions: "0644"
    content: |
      [Unit]
      Description=Hearthrack guided installer
      Conflicts=getty@tty8.service
      [Service]
      # The page on 127.0.0.1, and the text assistant on its own terminal (tty8: logind starts a login on tty1-6 when they are switched to),
      # for a screen that cannot show a page. tty1 stays the installer's own.
      ExecStart=/usr/local/bin/frame-installer --console /dev/tty8
      ExecStartPost=-/usr/bin/chvt 8
      Restart=on-failure
      StandardOutput=journal+console
      StandardError=journal+console
runcmd:
  - [ sh, -c, "install -m 0755 /cdrom/frame/installer /usr/local/bin/frame-installer && systemctl daemon-reload && systemctl start --no-block frame-installer" ]
YAML
} >"$w/server/user-data"
if grep -q 'PRIVATE KEY' "$w/server/user-data"; then echo "refusing: private key material in the seed" >&2; exit 1; fi
chmod 755 "$bin"

rm -f "$out"
xorriso -indev "$base" -outdev "$out" -map "$bin" /frame/installer \
  -map "$w/server/user-data" /server/user-data -map "$w/server/meta-data" /server/meta-data \
  "${maps[@]}" -boot_image any replay >/dev/null 2>"$w/xorriso.log" || { cat "$w/xorriso.log" >&2; exit 1; }
sha256sum "$out"
