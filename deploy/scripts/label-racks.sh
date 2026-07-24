#!/usr/bin/env bash
# Label each k8s node with the REAL physical host it runs on, so the Racks
# screen reflects physical topology instead of an invented rack tag.
#
# On a virtualized cluster the meaningful "rack" is the hypervisor host: every
# VM sharing one host shares its failure domain and oversubscribes its cores.
# We query Proxmox for the VM->host mapping + host capacity and stamp it onto
# the k8s nodes as topology.frame.io/* labels (the browser UI can't reach
# Proxmox, so the facts must live in k8s).
#
# Env: PVE_URL (https://host:8006), PVE_USER (root@pam), PVE_PASS.
# Node<->VM are matched by name (k8s node name == Proxmox VM name).
set -euo pipefail

PVE_URL="${PVE_URL:?set PVE_URL, e.g. https://192.168.2.1:8006}"
PVE_USER="${PVE_USER:-root@pam}"
PVE_PASS="${PVE_PASS:?set PVE_PASS}"

tok=$(curl -sk --data-urlencode "username=$PVE_USER" --data-urlencode "password=$PVE_PASS" \
  "$PVE_URL/api2/json/access/ticket" | grep -oP '"ticket":"\K[^"]+')
[ -n "$tok" ] || { echo "Proxmox auth failed" >&2; exit 1; }

curl -sk -H "Cookie: PVEAuthCookie=$tok" "$PVE_URL/api2/json/cluster/resources?type=vm" -o /tmp/pve-vms.json
curl -sk -H "Cookie: PVEAuthCookie=$tok" "$PVE_URL/api2/json/nodes" -o /tmp/pve-nodes.json

# name -> "host pcpu pmemGiB"
mapfile -t ROWS < <(python3 - <<'PY'
import json
hosts={n["node"]:(n.get("maxcpu",0), round(n.get("maxmem",0)/2**30)) for n in json.load(open("/tmp/pve-nodes.json"))["data"]}
for r in json.load(open("/tmp/pve-vms.json"))["data"]:
    h=r.get("node"); pcpu,pmem=hosts.get(h,(0,0))
    if r.get("name"): print(r["name"], h, pcpu, pmem)
PY
)

for kn in $(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'); do
  for row in "${ROWS[@]}"; do
    read -r name host pcpu pmem <<<"$row"
    if [ "$name" = "$kn" ]; then
      echo "label $kn -> rack=$host (${pcpu} pCPU / ${pmem} GiB)"
      kubectl label node "$kn" --overwrite \
        "topology.frame.io/rack=$host" \
        "topology.frame.io/hypervisor=proxmox" \
        "topology.frame.io/host-pcpu=$pcpu" \
        "topology.frame.io/host-pmem-gib=$pmem" >/dev/null
    fi
  done
done
echo "done."
