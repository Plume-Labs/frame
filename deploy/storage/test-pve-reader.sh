#!/usr/bin/env bash
# Replays the Proxmox reader's guards against the real manifest, under podman,
# with a fake Proxmox (TLS, throwaway CA) behind it. Nothing touches a cluster
# or a real Proxmox host.
#
#   deploy/storage/test-pve-reader.sh        # exit 0 = every case as expected
#
# What it proves, from deploy/storage/pve-reader.yaml as written (the
# ConfigMap is extracted from the file, not copied):
#   - whitelisted paths reach the upstream, with the token, under their
#     normalised URI and without the query string;
#   - everything else is 403, including traversals (plain, encoded, double-
#     encoded, backslash, empty segment) and non-GET methods;
#   - a VM config is reduced to smbios1 + disk slots;
#   - the container starts with a read-only root filesystem.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
manifest="${PVE_READER_MANIFEST:-$here/pve-reader.yaml}"  # override only to replay a mutation
image="$(awk '/image: docker.io\/nginxinc\/nginx-unprivileged@/ {print $2; exit}' "$manifest")"
work="$(mktemp -d)"
net="pve-reader-test-$$"
port="${PVE_READER_TEST_PORT:-18080}"
fail=0

cleanup() {
  podman rm -f "$net-reader" "$net-pve" >/dev/null 2>&1 || true
  podman network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# 1. The reader's config, straight from the manifest.
python3 - "$manifest" "$work" <<'PY'
import sys, yaml, os
manifest, work = sys.argv[1], sys.argv[2]
for doc in yaml.safe_load_all(open(manifest)):
    if doc and doc.get('kind') == 'ConfigMap' and doc['metadata']['name'] == 'frame-pve-reader-nginx':
        for k, v in doc['data'].items():
            open(os.path.join(work, k), 'w').write(v)
        break
else:
    sys.exit('ConfigMap frame-pve-reader-nginx not found')
PY

# 2. A throwaway CA and a server certificate for the fake Proxmox.
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=test-ca" \
  -keyout "$work/ca.key" -out "$work/pve-root-ca.pem" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj "/CN=pve-test" \
  -keyout "$work/pve.key" -out "$work/pve.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:pve-test\n' > "$work/san.ext"
openssl x509 -req -in "$work/pve.csr" -CA "$work/pve-root-ca.pem" -CAkey "$work/ca.key" \
  -CAcreateserial -days 1 -extfile "$work/san.ext" -out "$work/pve.crt" >/dev/null 2>&1

# 3. The fake Proxmox: echoes what it received; a VM config carries fields
#    the reader must strip.
cat > "$work/fake.conf" <<'NGINX'
server {
  listen 8006 ssl;
  ssl_certificate /fake/pve.crt;
  ssl_certificate_key /fake/pve.key;
  default_type application/json;
  location ~ /qemu/[0-9]+/config$ {
    return 200 '{"data":{"smbios1":"uuid=00000000-0000-4000-8000-000000000201","scsi2":"data:vm-201-disk-2,size=2T","ide2":"none,media=cdrom","net0":"virtio=00:00:00:00:00:01,bridge=vmbr0","cipassword":"leak","args":"-leak"}}';
  }
  location / {
    return 200 '{"data":{"uri":"$request_uri","auth":"$http_authorization"}}';
  }
}
NGINX
chmod -R a+r "$work"

podman network create "$net" >/dev/null
podman run -d --name "$net-pve" --network "$net" --network-alias pve-test \
  -v "$work/fake.conf:/etc/nginx/conf.d/default.conf:ro,Z" \
  -v "$work/pve.crt:/fake/pve.crt:ro,Z" -v "$work/pve.key:/fake/pve.key:ro,Z" \
  "$image" >/dev/null

podman run -d --name "$net-reader" --network "$net" -p "127.0.0.1:$port:8080" \
  --read-only --tmpfs /tmp:rw,mode=1777 --tmpfs /etc/nginx/conf.d:rw,mode=1777 \
  -e NGINX_ENVSUBST_FILTER='^PVE_' \
  -e PVE_HOST=pve-test -e PVE_TLS_NAME=pve-test -e 'PVE_TOKEN=frame-reader@pve!console=00000000-test' \
  -v "$work/nginx.conf:/etc/nginx/nginx.conf:ro,Z" \
  -v "$work/default.conf.template:/etc/nginx/templates/default.conf.template:ro,Z" \
  -v "$work/pve-reader.js:/etc/nginx/njs/pve-reader.js:ro,Z" \
  -v "$work/pve-root-ca.pem:/etc/pve-ca/pve-root-ca.pem:ro,Z" \
  "$image" >/dev/null

healthy=0
for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then healthy=1; break; fi
  sleep 0.2
done
if [[ $healthy != 1 ]]; then
  echo "FAIL  reader never became healthy"; podman logs "$net-reader" 2>&1 | tail -30
  exit 1
fi

base="http://127.0.0.1:$port"
N=host-01

# expect <status> <method> <path> [body substring that must appear] [substring that must not]
expect() {
  local want="$1" method="$2" path="$3" must="${4:-}" mustnot="${5:-}"
  local body code
  body="$(curl -s --path-as-is -X "$method" -o - -w '\n%{http_code}' "$base$path" || true)"
  code="${body##*$'\n'}"
  body="${body%$'\n'*}"
  local ok=1
  [[ "$code" == "$want" ]] || ok=0
  [[ -z "$must" || "$body" == *"$must"* ]] || ok=0
  [[ -z "$mustnot" || "$body" != *"$mustnot"* ]] || ok=0
  if [[ $ok == 1 ]]; then
    printf 'ok    %s %-4s %s\n' "$code" "$method" "$path"
  else
    printf 'FAIL  %s %-4s %s (want %s) body=%s\n' "$code" "$method" "$path" "$want" "$body"
    fail=1
  fi
}

# Allowed: reach the upstream with the token, under the normalised URI.
expect 200 GET "/api2/json/nodes" '"uri":"/api2/json/nodes"'
expect 200 GET "/api2/json/nodes/$N/disks/zfs" 'PVEAPIToken=frame-reader@pve!console=00000000-test'
expect 200 GET "/api2/json/nodes/$N/disks/zfs/data" '"uri":"/api2/json/nodes/host-01/disks/zfs/data"'
expect 200 GET "/api2/json/nodes/$N/disks/list" '"uri":"/api2/json/nodes/host-01/disks/list"'
expect 200 GET "/api2/json/nodes/$N/qemu" '"uri":"/api2/json/nodes/host-01/qemu"'
expect 200 GET "/api2/json/nodes/$N/storage/data/content" '"uri":"/api2/json/nodes/host-01/storage/data/content"'
# Query string dropped on the way up.
expect 200 GET "/api2/json/nodes/$N/disks/list?include-partitions=1&x=/access" '"uri":"/api2/json/nodes/host-01/disks/list"' 'include-partitions'
# VM config reduced to smbios1 + disk slots.
expect 200 GET "/api2/json/nodes/$N/qemu/201/config" '"scsi2":"data:vm-201-disk-2,size=2T"' 'net0'
expect 200 GET "/api2/json/nodes/$N/qemu/201/config" 'smbios1' 'leak'

# Refused: outside the whitelist.
expect 403 GET "/"
expect 403 GET "/api2/json/access/users"
expect 403 GET "/api2/json/cluster/resources"
expect 403 GET "/api2/json/nodes/$N/qemu/201/status/current"
expect 403 GET "/api2/json/nodes/$N/disks/zfs/data/extra"
# The internal subrequest prefix is not reachable from outside (internal → 404).
expect 404 GET "/__pve/api2/json/access/users"
# Refused: traversal, plain and encoded.
expect 403 GET "/api2/json/nodes/$N/disks/list/../../../access/users"
expect 403 GET "/api2/json/nodes/$N/disks/zfs/..%2F..%2F..%2Faccess%2Fusers"
expect 403 GET "/api2/json/nodes/$N/disks/zfs/%2E%2E/%2e%2e/access"
expect 403 GET "/api2/json/nodes/$N/disks/zfs/%252e%252e%252faccess"
expect 403 GET "/api2/json/nodes/$N/storage/data%2Fcontent"
expect 403 GET "/api2/json/nodes/$N/disks/zfs/..\\..\\access"
expect 403 GET "/api2/json/nodes//$N/disks/list"
expect 403 GET "/api2/json/nodes/$N/./disks/list"
expect 403 GET "/api2/json/nodes/$N/disks/zfs/.hidden"
# Refused: anything but GET.
expect 403 POST "/api2/json/nodes/$N/disks/zfs"
expect 403 PUT "/api2/json/nodes/$N/qemu/201/config"
expect 403 DELETE "/api2/json/nodes/$N/storage/data/content"

if [[ $fail != 0 ]]; then
  echo "--- reader log"; podman logs "$net-reader" 2>&1 | tail -20
  exit 1
fi
echo "all cases as expected"
