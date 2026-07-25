#!/usr/bin/env bash
# enable-k8s-audit.sh — RUN ON THE k3s CONTROL-PLANE NODE (sudo).
#
# Points the kube-apiserver audit log at the Falco k8saudit receiver (deployed by
# security-up.sh as NodePort 32765), so API-level attacks show up on the Frame
# Security screen (source=k8s_audit). Non-blocking (webhook batch mode) + auto-
# reverts if the apiserver doesn't come back, so a bad config can't brick the cp.
#
#   sudo ./enable-k8s-audit.sh            # from the repo, on the cp node
# or copy the two YAMLs + this script over and run it there.
set -euo pipefail
[ "$(id -u)" = "0" ] || { echo "run as root (sudo)"; exit 1; }

D=/var/lib/rancher/k3s/server
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../samples/test-cluster" && pwd)"
NODEPORT="${AUDIT_NODEPORT:-32765}"

install -m600 "$HERE/k8s-audit-policy.yaml" "$D/audit-policy.yaml"
cat > "$D/audit-webhook.yaml" <<WH
apiVersion: v1
kind: Config
clusters:
  - name: falco
    cluster: { server: "http://127.0.0.1:${NODEPORT}/k8s-audit" }
contexts:
  - name: falco
    context: { cluster: falco, user: "" }
current-context: falco
WH

CFG=/etc/rancher/k3s/config.yaml
[ -f "$CFG" ] && cp "$CFG" "$CFG.bak.$(date +%s 2>/dev/null || echo bak)" || true
cat > "$CFG" <<'CFGEOF'
kube-apiserver-arg:
  - "audit-policy-file=/var/lib/rancher/k3s/server/audit-policy.yaml"
  - "audit-webhook-config-file=/var/lib/rancher/k3s/server/audit-webhook.yaml"
  - "audit-webhook-mode=batch"
  - "audit-webhook-batch-max-wait=5s"
CFGEOF

echo "restarting k3s…"
systemctl restart k3s
for i in $(seq 1 18); do
  if k3s kubectl get --raw='/readyz' >/dev/null 2>&1; then
    echo "APISERVER OK — audit → Falco k8saudit enabled."
    exit 0
  fi
  sleep 5
done
echo "APISERVER did not return — reverting audit config."
rm -f "$CFG"
ls "$CFG".bak.* >/dev/null 2>&1 && mv "$(ls -t "$CFG".bak.* | head -1)" "$CFG" || true
systemctl restart k3s
echo "reverted."
exit 1
