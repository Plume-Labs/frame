# PXE Boot Configuration

This directory contains configurations for PXE (Preboot Execution Environment) network booting.

## Overview

PXE boot enables bare metal servers to boot from the network and automatically provision operating systems. This is essential for:
- Rapid cluster expansion
- Hot hardware updates
- Consistent OS deployment
- Automated infrastructure provisioning

## Directory Structure

```
pxe/
├── tftp/                  # TFTP boot files
│   ├── pxelinux.cfg/     # PXE boot menu configurations
│   └── images/           # Kernel images and initrd
├── dhcp/                  # DHCP server configuration
├── kickstart/            # Kickstart files for automated installation
└── preseed/              # Preseed files (for Debian/Ubuntu)
```

## Configuration

The Talos/Sidero provisioning stack configures:

1. **TFTP Server** - Serves boot files to PXE clients
2. **DHCP Server** - Provides network configuration and boot filename
3. **NFS/HTTP Server** - Hosts installation media
4. **Boot Menu** - Presents OS installation options

## Boot Flow

```
┌──────────────┐
│ Bare Metal   │
│   Server     │
└──────┬───────┘
       │ 1. Power on / PXE boot
       ▼
┌──────────────┐
│ DHCP Server  │  2. Assigns IP, provides boot filename
└──────┬───────┘
       ▼
┌──────────────┐
│ TFTP Server  │  3. Downloads pxelinux.0 and boot menu
└──────┬───────┘
       ▼
┌──────────────┐
│ Boot Menu    │  4. User selects OS or auto-provision
└──────┬───────┘
       ▼
┌──────────────┐
│ HTTP/NFS     │  5. Downloads kernel, initrd, packages
└──────┬───────┘
       ▼
┌──────────────┐
│ Kickstart/   │  6. Automated installation
│  Preseed     │
└──────┬───────┘
       ▼
┌──────────────┐
│ Installed OS │  7. System reboots, joins cluster
└──────────────┘
```

## IPMI Integration

For completely automated provisioning:

```bash
# Set next boot to PXE
ipmitool -I lanplus -H <BMC_IP> -U <USER> -P <PASS> \
  chassis bootdev pxe options=persistent

# Power on/cycle the server
ipmitool -I lanplus -H <BMC_IP> -U <USER> -P <PASS> \
  power cycle
```

This can be integrated into Sidero workflows for zero-touch provisioning.

## Network Requirements

- Dedicated provisioning network (e.g., 192.168.1.0/24)
- DHCP range for new nodes
- PXE server accessible on port 69 (TFTP), 80 (HTTP), 2049 (NFS)
- BMC/IPMI network for out-of-band management

## Supported OS

The PXE configuration supports:
- Ubuntu 22.04 LTS (recommended)
- Ubuntu 20.04 LTS
- Rocky Linux 9
- Debian 12

## Customization

### Add Custom Boot Entry

Edit `pxelinux.cfg/default`:

```
LABEL custom-ubuntu
  MENU LABEL Ubuntu 22.04 Custom Kernel
  KERNEL images/ubuntu-22.04/vmlinuz
  APPEND initrd=images/ubuntu-22.04/initrd.img ...
```

### Kickstart/Preseed

Customize `kickstart/k8s-node.ks` or `preseed/k8s-node.cfg` to:
- Set hostname patterns
- Configure disk partitioning
- Install additional packages
- Run post-installation scripts

## Security

PXE boot can be secured with:
- VLAN isolation for provisioning network
- MAC address filtering in DHCP
- HTTPS for installation media
- Signed kernels and initrd

## Troubleshooting

### Node not booting PXE

1. Check BIOS/UEFI boot order
2. Verify network cable and switch port
3. Check DHCP server logs: `journalctl -u dnsmasq -f`
4. Verify TFTP files: `ls -la /var/lib/tftpboot/`

### Boot hangs after PXE

1. Check kernel/initrd paths in boot menu
2. Verify NFS/HTTP server is accessible
3. Check kickstart/preseed syntax
4. Review installation logs on the node console
