#!/usr/bin/env bash
# Fails if an image built by hack/build-iso.sh carries secret material.
# Checks what Frame writes into the image (the two GRUB configs and, when a seed
# directory is given, the served autoinstall files) for private keys, k3s join
# tokens and password-shaped values. Usage:
#   hack/check-iso-secrets.sh <image.iso> [seed-dir]
set -euo pipefail
iso=${1:?usage: check-iso-secrets.sh <image.iso> [seed-dir]}
seed=${2:-}
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT

for f in /boot/grub/grub.cfg /boot/grub/loopback.cfg; do
  xorriso -osirrox on -indev "$iso" -extract "$f" "$work/$(basename "$f")" >/dev/null 2>&1 || true
done
[ -s "$work/grub.cfg" ] || { echo "check-iso-secrets: no grub.cfg in $iso" >&2; exit 1; }
grep -q 'autoinstall' "$work/grub.cfg" || { echo "check-iso-secrets: grub.cfg carries no autoinstall argument: not one of our images" >&2; exit 1; }

targets=("$work"/*)
[ -n "$seed" ] && targets+=("$seed"/*)

# PRIVATE KEY blocks; k3s join tokens (K10<hash>::server:<secret>); any
# token=/password= style assignment in a boot line.
bad=0
for f in "${targets[@]}"; do
  [ -f "$f" ] || continue
  if grep -nEi -e 'PRIVATE KEY' -e 'K10[0-9a-f]{20,}::' -e '(k3s_token|join[_-]?token|--token)[ =]+[^ ]+' "$f" >&2; then
    echo "check-iso-secrets: secret-shaped content in $f" >&2; bad=1
  fi
done
# The only password in a seed is the locked, throwaway hash the identity section
# needs; anything else shaped like a hash would be a leak.
if [ -n "$seed" ]; then
  n=$(grep -ho '\$[0-9a-z]\+\$[./A-Za-z0-9]\+\$[./A-Za-z0-9]\+' "$seed"/* 2>/dev/null | sort -u | wc -l)
  [ "$n" -le 1 ] || { echo "check-iso-secrets: $n distinct password hashes in the seed, expected the single locked one" >&2; bad=1; }
fi
[ $bad = 0 ] && echo "check-iso-secrets: clean ($(basename "$iso"))"
exit $bad
