# The ceph-mgr dashboard account behind the Storage screen

The Storage screen shows, per pool, `min_size` and the CRUSH rule, and per
OSD the CRUSH weight. Prometheus exports none of these (`ceph_osd_weight`
is the reweight, not the CRUSH weight). The Frame manager reads them from
the Ceph dashboard's REST API (`internal/cephmgr`), every 5 minutes, into
`status.cephPlacement` of each `ceph-*` FrameStorage entry.

Until the steps below are done, the screen says "not collected (no read-only
dashboard account configured)". Nothing breaks.

## What the manager sends, and nothing else

- `POST /api/auth`: this swaps the account's password for a token. The
  dashboard has no other way to log in.
- `GET /api/pool`, `GET /api/osd` and `GET /api/crush_rule`: exactly these
  paths, with no parameters. Any other path is refused in code, before it
  leaves the process.
- The connection is HTTPS only. The server certificate is verified against
  the one stored in the Secret (pinned), under an explicit server name. A
  plain-HTTP URL is refused.

## Why a custom role, not `read-only`

The built-in `read-only` role grants read on every dashboard scope. That
includes configuration options, logs, hosts, users of RGW, the NFS
exports and the audit trail. Frame needs two scopes. `/api/pool` and
`/api/crush_rule` sit under the `pool` scope, and `/api/osd` under the `osd`
scope. The role below grants `read` on those two scopes and nothing else.
With it, even a request the code would never send could not read more.

## Steps (to validate, then run at deployment)

The dashboard is served in plain HTTP today (`spec.dashboard` has no `ssl`,
port 7000). The password must not cross the network in clear text, so TLS
comes first.

```sh
NS=rook-ceph
TOOLS="kubectl -n $NS exec deploy/rook-ceph-tools --"
SVC=rook-ceph-mgr-dashboard.$NS.svc

# 1. A certificate for the dashboard, naming its Service (Go verifies the
#    subjectAltName, not the CN).
umask 077
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 825 \
  -subj "/CN=$SVC" -addext "subjectAltName=DNS:$SVC,DNS:$SVC.cluster.local" \
  -keyout dashboard.key -out dashboard.crt
kubectl -n $NS exec -i deploy/rook-ceph-tools -- sh -c 'umask 077; cat > /tmp/dashboard.crt' < dashboard.crt
kubectl -n $NS exec -i deploy/rook-ceph-tools -- sh -c 'umask 077; cat > /tmp/dashboard.key' < dashboard.key
$TOOLS ceph dashboard set-ssl-certificate -i /tmp/dashboard.crt
$TOOLS ceph dashboard set-ssl-certificate-key -i /tmp/dashboard.key
$TOOLS rm -f /tmp/dashboard.crt /tmp/dashboard.key

# 2. Serve the dashboard in TLS (Rook moves it to port 8443).
kubectl -n $NS patch cephcluster rook-ceph --type merge -p '{"spec":{"dashboard":{"ssl":true}}}'
kubectl -n $NS get svc rook-ceph-mgr-dashboard   # port 8443

# 3. The role: read on the pool and osd scopes, nothing else.
$TOOLS ceph dashboard ac-role-create frame-storage-reader "Frame console: pools and OSDs, read only"
$TOOLS ceph dashboard ac-role-add-scope-perms frame-storage-reader pool read
$TOOLS ceph dashboard ac-role-add-scope-perms frame-storage-reader osd read
$TOOLS ceph dashboard ac-role-show frame-storage-reader

# 4. The account, with a password read from a file (never on a command line).
openssl rand -base64 32 | tr -d '\n' > frame-mgr.pass
kubectl -n $NS exec -i deploy/rook-ceph-tools -- sh -c 'umask 077; cat > /tmp/frame-mgr.pass' < frame-mgr.pass
$TOOLS ceph dashboard ac-user-create frame-storage-reader -i /tmp/frame-mgr.pass frame-storage-reader
$TOOLS rm -f /tmp/frame-mgr.pass

# 5. The Secret the manager reads (its own namespace).
kubectl -n frame-system create secret generic frame-ceph-dashboard \
  --from-literal=url=https://$SVC:8443 \
  --from-literal=username=frame-storage-reader \
  --from-file=password=frame-mgr.pass \
  --from-file=ca.crt=dashboard.crt \
  --from-literal=serverName=$SVC
shred -u frame-mgr.pass dashboard.key
```

Within 5 minutes, `kubectl get framestorage -o jsonpath='{.items[*].status.cephPlacement.state}'`
says `Collected`.

If a step fails, the screen shows why: `Error` with the message, and it
keeps the last good read with its date. The manager needs no new RBAC:
it already reads Secrets.

To remove access: `ceph dashboard ac-user-delete frame-storage-reader`, then
`kubectl -n frame-system delete secret frame-ceph-dashboard`. The state goes
back to `NotConfigured`.
