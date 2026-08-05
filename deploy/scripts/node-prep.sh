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
#   - agent=1 + startup=order=N,up=…   (clean shutdown + boot order; see below)
set -euo pipefail

# Total time kubelet may spend terminating pods when the node is shutting down,
# and the slice of it reserved for critical pods. logind must be told to wait at
# least this long before it kills everything, so keep INHIBIT_DELAY the larger.
SHUTDOWN_GRACE="${SHUTDOWN_GRACE:-90s}"
SHUTDOWN_GRACE_CRITICAL="${SHUTDOWN_GRACE_CRITICAL:-30s}"
INHIBIT_DELAY="${INHIBIT_DELAY:-120}"

BURST_DEV="${1:-}"   # optional: e.g. /dev/sdc (the ssd=1 disk) to mount as burst buffer

# ── Graceful node shutdown ────────────────────────────────────────────────────
# Without this, a reboot kills kubelet outright: pods are never terminated, so
# volumes are never unmounted and containers never finish stopping. What that
# leaves behind on the next boot is a cluster that needs hand-repair — a RWO
# VolumeAttachment still pinned to the node that went away (every pod using it
# then hits "Multi-Attach error"), and containerd holding name reservations for
# containers that no longer exist ("name … is reserved for <id>", which strands
# the CSI driver-registrar and so every Ceph mount on that node).
#
# kubelet only gets the shutdown signal if it can take a systemd inhibitor lock
# and hold it long enough to drain, hence both halves below.
KUBELET_CONF_D=/var/lib/rancher/k3s/agent/etc/kubelet.conf.d   # k3s v1.32+ drop-ins
mkdir -p "$KUBELET_CONF_D"
cat > "$KUBELET_CONF_D/10-graceful-shutdown.conf" <<CONF
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
shutdownGracePeriod: $SHUTDOWN_GRACE
shutdownGracePeriodCriticalPods: $SHUTDOWN_GRACE_CRITICAL
CONF

mkdir -p /etc/systemd/logind.conf.d
rm -f /etc/systemd/logind.conf.d/10-k3s-shutdown.conf   # pre-fix name, sorted too early
# systemd orders drop-ins by FILENAME across every search dir, last one wins —
# living in /etc is not enough. Ubuntu ships
# /usr/lib/systemd/logind.conf.d/unattended-upgrades-logind-maxdelay.conf
# (InhibitDelayMaxSec=30), and "unattended-…" sorts after both "10-…" and the
# "99-kubelet.conf" kubelet writes itself, so it silently wins and kubelet then
# fails with "timed out … waiting for logind InhibitDelayMaxSec to update".
# The zz- prefix is what makes this stick.
cat > /etc/systemd/logind.conf.d/zz-k3s-graceful-shutdown.conf <<CONF
# Must exceed kubelet's shutdownGracePeriod ($SHUTDOWN_GRACE) or logind kills
# the node mid-drain and the graceful shutdown is worthless.
[Login]
InhibitDelayMaxSec=$INHIBIT_DELAY
CONF
systemctl restart systemd-logind

EFFECTIVE=$(busctl get-property org.freedesktop.login1 /org/freedesktop/login1 \
  org.freedesktop.login1.Manager InhibitDelayMaxUSec 2>/dev/null | awk '{print $2/1000000}')
echo "logind: InhibitDelayMaxSec effective=${EFFECTIVE:-?}s (needs >= $SHUTDOWN_GRACE)"
echo "shutdown: grace=$SHUTDOWN_GRACE critical=$SHUTDOWN_GRACE_CRITICAL inhibit=${INHIBIT_DELAY}s (kubelet picks it up on next k3s start)"

# ── qemu-guest-agent: let Proxmox see when the guest is actually down ─────────
# With agent=1 on the VM but no agent running in it, the host cannot tell a
# still-draining guest from a hung one, so a host reboot stops waiting and pulls
# the plug — which is what produced the wreckage the section above cleans up.
if ! systemctl is-active --quiet qemu-guest-agent 2>/dev/null; then
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq qemu-guest-agent >/dev/null 2>&1 || true
  systemctl enable --now qemu-guest-agent >/dev/null 2>&1 || true
fi
echo "guest-agent: $(systemctl is-active qemu-guest-agent 2>/dev/null || echo unavailable)"

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
