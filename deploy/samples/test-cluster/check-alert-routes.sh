#!/usr/bin/env bash
# Verifies the Alertmanager routing in kps-values.yaml with amtool, offline.
# Every alert must reach Frame's receiver; critical ones go through the
# `critical` receiver, which also points at Frame.
set -euo pipefail
cd "$(dirname "$0")"
command -v amtool >/dev/null || { echo "amtool missing (github.com/prometheus/alertmanager releases)" >&2; exit 1; }
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
python3 -c 'import sys,yaml; yaml.safe_dump(yaml.safe_load(open("kps-values.yaml"))["alertmanager"]["config"], sys.stdout)' \
  | sed 's#/etc/alertmanager/secrets/frame-alert-receiver-token/token#/etc/hostname#' > "$tmp"
amtool check-config "$tmp" >/dev/null
fail=0
check() { # expected-receivers label=value...
  local want=$1; shift
  local got; got=$(amtool config routes test --config.file="$tmp" "$@" | tail -1)
  if [[ "$got" == "$want" ]]; then echo "ok   $* -> $got"; else echo "FAIL $* -> $got (want $want)"; fail=1; fi
}
check critical severity=critical alertname=CephHealthError
check default severity=warning alertname=KubeCPUOvercommit
check default alertname=Watchdog
grep -q 'frame-alert-receiver.frame-system.svc' "$tmp" || { echo "FAIL receivers do not point at Frame"; fail=1; }
grep -q 'alert-sink' "$tmp" && { echo "FAIL alert-sink still configured"; fail=1; }
exit $fail
