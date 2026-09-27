#!/bin/bash

echo "Making deployment scripts executable..."

chmod +x deploy/scripts/health-check.sh
chmod +x deploy/gitops/bootstrap-flux.sh

echo "✅ All scripts are now executable!"
echo ""
echo "Available scripts:"
echo "  deploy/scripts/health-check.sh     - Check cluster health"
echo "  deploy/gitops/bootstrap-flux.sh    - Initialize Flux GitOps"
