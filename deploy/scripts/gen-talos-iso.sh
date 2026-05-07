#!/usr/bin/env bash
# Downloads the vanilla Talos ISO for bare-metal boot (maintenance mode)
# Usage: ./gen-talos-iso.sh [version] [arch]
VERSION=${1:-v1.9.0}
ARCH=${2:-amd64}
URL="https://github.com/siderolabs/talos/releases/download/${VERSION}/talos-${ARCH}.iso"
echo "Downloading Talos ${VERSION} ISO for ${ARCH}..."
curl -L -o "talos-${ARCH}.iso" "$URL"
echo "Done. Flash to USB: dd if=talos-${ARCH}.iso of=/dev/sdX bs=4M status=progress"
