#!/usr/bin/env bash
# Enrols one or more FIDO2 keys (YubiKey) for the local console login of nodes
# built with hack/build-iso.sh --u2f-keys. Run it on YOUR machine, not on a node:
# it asks you to touch each key, and writes the PUBLIC credentials to a file.
#
#   hack/enroll-yubikey.sh [-o u2f-keys.txt] [count]     # default: 2 keys (one spare)
#
# What it needs: pamu2fcfg (apt install pamu2fcfg), and a PIN already set on each
# key (ykman fido access change-pin). The console login requires key + PIN, and
# enrolling with the PIN requirement fails on a key that has none: refusing here
# is better than finding out at a console.
#
# The file holds, per key, a key handle and a public key: nothing that opens
# anything without the physical key and its PIN. It is still yours: do not
# publish it. NOTE: a FIDO2 registration yields a different key handle every time,
# so enrolling the SAME physical key twice looks like two keys: the script cannot
# tell. Plug in a different key for each round, or the "spare" is not a spare. Every credential is bound to the origin pam://hearthrack (the same
# constant as internal/provision/u2f.go), so one key works on every node.
set -euo pipefail

out=u2f-keys.txt
if [ "${1:-}" = "-o" ]; then out=${2:?-o needs a file}; shift 2; fi
count=${1:-2}
[[ "$count" =~ ^[1-4]$ ]] || { echo "enroll-yubikey: count must be 1 to 4" >&2; exit 1; }
command -v pamu2fcfg >/dev/null || { echo "enroll-yubikey: pamu2fcfg not found (apt install pamu2fcfg)" >&2; exit 1; }
origin=pam://hearthrack

: >"$out.tmp"
trap 'rm -f "$out.tmp"' EXIT
for i in $(seq "$count"); do
  echo "Key $i of $count: plug it in (a DIFFERENT physical key each round, and no other key plugged in), enter its PIN if asked, then touch it when it blinks."
  line=$(pamu2fcfg -u frame -o "$origin" -i "$origin" -N) || { echo "enroll-yubikey: enrolment of key $i failed (no PIN set on it?)" >&2; exit 1; }
  cred=${line#frame:}
  [ "$cred" != "$line" ] && [[ "$cred" != *$'\n'* ]] && [[ "$cred" =~ ^[A-Za-z0-9+/_=-]+,[A-Za-z0-9+/_=-]+,(es256|rs256|eddsa),[a-z+-]*$ ]] \
    || { echo "enroll-yubikey: unexpected pamu2fcfg output for key $i" >&2; exit 1; }
  echo "$cred" >>"$out.tmp"
  [ "$i" -lt "$count" ] && read -r -p "Unplug it, plug the next one, press Enter. " _
done
mv "$out.tmp" "$out"; chmod 600 "$out"
trap - EXIT
echo "Wrote $count credential(s) to $out. Build with: hack/build-iso.sh ... --u2f-keys $out"
echo "Test one on a real node before relying on it; SSH (key only) stays your way in if the console login fails."
