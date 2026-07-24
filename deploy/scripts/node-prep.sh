#!/usr/bin/env bash
# node-prep.sh — per-node prerequisites for the virtualized test cluster.
# Run once on EACH k3s node (SSH). Idempotent.
#
#   scp deploy/scripts/node-prep.sh neura@<node>:/tmp/ && ssh neura@<node> 'sudo bash /tmp/node-prep.sh'
#
# Proxmox-side prerequisites (not done here — set on the VM before boot):
#   - cpu=host                         (Ceph v18+ needs x86-64-v2)
#   - a raw disk on scsi2              (Ceph OSD)
#   - an ssd=1 disk (e.g. scsi3)       (burst-buffer tier) — pass its device below
set -euo pipefail

BURST_DEV="${1:-}"   # optional: e.g. /dev/sdc (the ssd=1 disk) to mount as burst buffer

# ── ptp_kvm: paravirtual PTP hardware clock, persistent across reboots ────────
modprobe ptp_kvm || true
echo ptp_kvm > /etc/modules-load.d/ptp_kvm.conf
echo "ptp_kvm: loaded=$(lsmod | grep -c ptp_kvm), /dev/ptp0=$(ls /dev/ptp0 2>/dev/null || echo absent)"

# ── KSM: enable kernel same-page merging (KSM screen) ─────────────────────────
echo 1 > /sys/kernel/mm/ksm/run || true
echo 1000 > /sys/kernel/mm/ksm/pages_to_scan || true
echo 200 > /sys/kernel/mm/ksm/sleep_millisecs || true
cat > /etc/systemd/system/ksm-enable.service <<'UNIT'
[Unit]
Description=Enable KSM
[Service]
Type=oneshot
ExecStart=/bin/sh -c 'echo 1 > /sys/kernel/mm/ksm/run; echo 1000 > /sys/kernel/mm/ksm/pages_to_scan; echo 200 > /sys/kernel/mm/ksm/sleep_millisecs'
[Install]
WantedBy=multi-user.target
UNIT
systemctl enable ksm-enable.service >/dev/null 2>&1 || true
echo "ksm: run=$(cat /sys/kernel/mm/ksm/run)"

# ── Burst-buffer SSD mount, persistent by UUID ────────────────────────────────
if [ -n "$BURST_DEV" ] && [ -b "$BURST_DEV" ]; then
  if ! blkid -s UUID -o value "$BURST_DEV" >/dev/null 2>&1; then
    mkfs.ext4 -q -F "$BURST_DEV"
  fi
  UUID=$(blkid -s UUID -o value "$BURST_DEV")
  mkdir -p /burst-buffer
  grep -q "$UUID" /etc/fstab || echo "UUID=$UUID /burst-buffer ext4 defaults,nofail 0 2" >> /etc/fstab
  mount -a
  chmod 777 /burst-buffer
  echo "burst: $(findmnt -no SOURCE,TARGET /burst-buffer 2>/dev/null || echo not-mounted)"
else
  echo "burst: skipped (pass the ssd device as arg 1, e.g. /dev/sdc)"
fi

# ── NVIDIA GPU driver (only on a node with a passed-through NVIDIA GPU) ────────
# Requires the Proxmox host to have done PCI passthrough (see docs/test-cluster.md
# "GPU passthrough"): vfio-pci bound at boot + Secure Boot OFF, else the DKMS
# module fails to load ("Key was rejected by service"). We install the driver on
# the node; the GPU operator (bring-up, GPU=1) runs with driver.enabled=false.
if lspci 2>/dev/null | grep -qi 'NVIDIA'; then
  if command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi >/dev/null 2>&1; then
    echo "gpu: driver already working — $(nvidia-smi --query-gpu=name,driver_version --format=csv,noheader | head -1)"
  else
    echo "gpu: NVIDIA device present, installing driver…"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq nvidia-driver-580-server >/dev/null 2>&1 \
      || apt-get install -y -qq nvidia-driver-server >/dev/null 2>&1 \
      || echo "gpu: driver package install failed — install the vendor driver manually"
    nvidia-smi >/dev/null 2>&1 \
      && echo "gpu: OK — $(nvidia-smi --query-gpu=name,driver_version --format=csv,noheader | head -1)" \
      || echo "gpu: module not loaded — check Secure Boot is OFF (dmesg | grep -i 'key was rejected')"
  fi
else
  echo "gpu: no NVIDIA device on this node — skipping driver"
fi
