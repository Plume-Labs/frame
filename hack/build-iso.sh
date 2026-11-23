#!/usr/bin/env bash
# Builds an unattended Ubuntu 24.04 installer ISO for ONE machine, to burn on a
# USB key. Runs outside any cluster: it starts frame-provisiond, asks it for an
# image, and leaves it serving the autoinstall seed the image fetches at boot.
#
# The machine needs DHCP while it installs (the live system fetches its seed
# before its own network section applies); --dhcp only says what address the
# INSTALLED system uses. Keep this script's provisiond running until the machine
# answers on SSH, then run it again with --stop.
#
# Usage:
#   hack/build-iso.sh --hostname tacos --boot bios --ssh-key ~/.ssh/id_ed25519.pub \
#       --disk /dev/disk/by-id/scsi-3600...:600127000000 --media-ip 192.168.2.210 \
#       (--dhcp | --address 192.168.2.202/24 --gateway 192.168.2.254 --dns 192.168.2.254)
#   hack/build-iso.sh --stop
#
# --u2f-keys FILE (from hack/enroll-yubikey.sh) enables a YubiKey + PIN login on the
# local console. Two --disk arguments make a RAID 1 mirror. Find the disk with hack/list-disks.sh.
#
# Environment: MEDIA_BIND (listen address, default --media-ip), OUT (default ./iso-out), FRAME_PROVISIOND_BIN (a provisiond
# binary to run directly; default: podman build + run of Dockerfile.provisiond),
# FRAME_BASE_ISO_URL is NOT honoured here (provisiond fetches the pinned base).
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
OUT=${OUT:-$PWD/iso-out}
NAME=frame-provisiond-iso
die() { echo "build-iso: $*" >&2; exit 1; }

stop() {
  podman rm -f "$NAME" >/dev/null 2>&1 || true
  [ -f "$OUT/provisiond.pid" ] && kill "$(cat "$OUT/provisiond.pid")" 2>/dev/null || true
  rm -f "$OUT/provisiond.pid"
  echo "provisiond stopped."
}

u2f_file="" hostname="" boot="" key="" media_ip="" dhcp=0 address="" gateway="" dns=() disks=() build_port=${BUILD_PORT:-8080} media_port=${MEDIA_PORT:-8081}
while [ $# -gt 0 ]; do
  case "$1" in
    --stop) stop; exit 0 ;;
    --hostname) hostname=$2; shift 2 ;;
    --boot) boot=$2; shift 2 ;;
    --ssh-key) key=$2; shift 2 ;;
    --media-ip) media_ip=$2; shift 2 ;;
    --disk) disks+=("$2"); shift 2 ;;
    --dhcp) dhcp=1; shift ;;
    --address) address=$2; shift 2 ;;
    --gateway) gateway=$2; shift 2 ;;
    --dns) dns+=("$2"); shift 2 ;;
    --u2f-keys) u2f_file=$2; shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done

[ -n "$hostname" ] || die "--hostname is required"
case "$boot" in bios) mode=Legacy ;; uefi) mode=UEFI ;; *) die "--boot must be bios or uefi (the disk layout differs; the machine's setup screen says which it uses)" ;; esac
[ -r "$key" ] || die "--ssh-key must be a readable PUBLIC key file"
grep -q 'PRIVATE KEY' "$key" && die "$key is a private key: give the .pub file"
[ -n "$media_ip" ] || die "--media-ip is required: the address this host has on the machine's network"
[ ${#disks[@]} -ge 1 ] && [ ${#disks[@]} -le 2 ] || die "give one or two --disk <by-id>:<size-in-bytes> (hack/list-disks.sh prints them)"
if [ $dhcp = 1 ]; then
  [ -z "$address$gateway" ] && [ ${#dns[@]} -eq 0 ] || die "--dhcp excludes --address, --gateway and --dns"
else
  [ -n "$address" ] && [ -n "$gateway" ] && [ ${#dns[@]} -ge 1 ] || die "give --dhcp, or all of --address, --gateway and --dns"
fi
command -v jq >/dev/null || die "jq is required"

disk_json='[]'
for d in "${disks[@]}"; do
  id=${d%:*}; size=${d##*:}
  [[ "$id" == /dev/disk/by-id/* ]] || die "disk $id: must be a /dev/disk/by-id/ path"
  [[ "$size" =~ ^[0-9]+$ ]] || die "disk $d: expected <by-id>:<size-in-bytes>"
  disk_json=$(jq -c --arg id "$id" --argjson s "$size" '. + [{ByID:$id, SizeBytes:$s}]' <<<"$disk_json")
done
kind=single-disk; [ ${#disks[@]} -eq 2 ] && kind=mirror

if [ $dhcp = 1 ]; then
  net='{"DHCP": true}'
else
  dns_json=$(printf '%s\n' "${dns[@]}" | jq -R . | jq -sc .)
  net=$(jq -nc --arg a "$address" --arg g "$gateway" --argjson d "$dns_json" '{Address:$a, Gateway:$g, DNS:$d}')
fi
u2f_json='[]'
if [ -n "$u2f_file" ]; then
  [ -r "$u2f_file" ] || die "--u2f-keys: cannot read $u2f_file (hack/enroll-yubikey.sh writes it)"
  u2f_json=$({ grep -vE '^[[:space:]]*(#|$)' "$u2f_file" || true; } | jq -R . | jq -sc .)
  [ "$u2f_json" != '[]' ] || die "--u2f-keys: $u2f_file holds no credential"
fi
uid=$(cat /proc/sys/kernel/random/uuid)
spec=$(jq -nc --arg uid "$uid" --arg host "$hostname" --arg mode "$mode" --arg kind "$kind" \
  --argjson disks "$disk_json" --argjson net "$net" --arg key "$(cat "$key")" --argjson u2f "$u2f_json" \
  '{UID:$uid, Hostname:$host, Network:$net, Layout:{Kind:$kind, Disks:$disks}, SSHPublicKey:$key, U2FKeys:$u2f, BootMode:$mode}')

bind=${MEDIA_BIND:-$media_ip}   # where provisiond listens; differs from --media-ip only behind NAT (QEMU bench)
mkdir -p "$OUT/images"
stop >/dev/null
if [ -n "${FRAME_PROVISIOND_BIN:-}" ]; then
  MEDIA_URL="http://$media_ip:$media_port" IMAGES_DIR="$OUT/images" BUILD_ADDR="127.0.0.1:$build_port" MEDIA_ADDR="$bind:$media_port" \
    nohup "$FRAME_PROVISIOND_BIN" >"$OUT/provisiond.log" 2>&1 &
  echo $! >"$OUT/provisiond.pid"
else
  podman build -q -f "$here/Dockerfile.provisiond" -t frame-provisiond:iso "$here" >/dev/null
  podman run -d --name "$NAME" --userns=keep-id:uid=65532,gid=65532 \
    -p "127.0.0.1:$build_port:8080" -p "$bind:$media_port:8081" \
    -e MEDIA_URL="http://$media_ip:$media_port" -v "$OUT/images:/var/lib/frame/images:Z" frame-provisiond:iso >/dev/null
fi
for _ in $(seq 30); do curl -fsS "http://$bind:$media_port/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "http://$bind:$media_port/healthz" >/dev/null || die "provisiond did not come up (see $OUT/provisiond.log or podman logs $NAME)"

echo "==> building (the first run downloads the ~4 GB Ubuntu base image and checks its SHA256)"
resp=$(curl -sS --max-time 3600 -X POST -H 'Content-Type: application/json' --data "$spec" "http://127.0.0.1:$build_port/build") \
  || die "build request failed"
token=$(jq -r .token <<<"$resp" 2>/dev/null || true)
[[ "$token" =~ ^[a-f0-9]{32}$ ]] || die "build refused: $resp"

iso="$OUT/$hostname.iso"
rm -f "$iso" "$iso.sha256"
# Checked BEFORE the image is copied to a name someone will burn.
"$here/hack/check-iso-secrets.sh" "$OUT/images/$token.iso" "$OUT/images/$token" || { stop >/dev/null; die "secret check failed, no image written"; }
cp "$OUT/images/$token.iso" "$iso"
( cd "$OUT" && sha256sum "$hostname.iso" | tee "$hostname.iso.sha256" )
cat <<MSG

ISO ready: $iso
  install UID: $uid  (the installed system's /etc/frame-install-uid must say this)
  console key login: $([ -n "$u2f_file" ] && echo "YubiKey + PIN ($(jq length <<<"$u2f_json") key(s))" || echo "none")
  mode: $mode, disks: ${#disks[@]} ($kind), network: $([ $dhcp = 1 ] && echo DHCP || echo "$address")
Burn it:   sudo dd if=$iso of=/dev/sdX bs=4M status=progress conv=fsync   (sdX = the USB key)
Boot the machine from the key; it installs with no input and reboots. It fetches
its seed from http://$media_ip:$media_port, so leave provisiond running until
'ssh frame@<address>' answers, then: $0 --stop
<address> is the one you gave, or in DHCP mode the lease: read it on the router or the machine's console.
MSG
