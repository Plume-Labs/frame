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
- `GET /api/pool`, `GET /api/osd?limit=-1` and `GET /api/crush_rule`:
  exactly these, each with the API version its endpoint declares (1.0,
  1.1 and 2.0). The only query is the fixed `limit=-1`, because the OSD
  list is paged at 10 by default. Any other path or query is refused in
  code, before it leaves the process.
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

## A refused account: no automatic retry

Ceph counts failed logins per user, and only a successful login resets
the count (`dashboard/controllers/auth.py`). At
`ceph dashboard get-account-lockout-attempts` failures (10 by default),
the account is disabled. Any retry schedule, however spaced out, ends in
a locked account. Ceph answers a refused login with HTTP 400 and
`"code": "invalid_credentials"`.

So the manager:

- stops the pass at the first failed login: no GET of that pass logs in
  again;
- publishes `AuthRefused`, along with the refused account's
  `refusedCredentials` (a truncated SHA-256 of url, username and password,
  never the password itself);
- never logs in again with that account. It tries again only when the
  account's content in the Secret changes (url, username or password). An
  annotation or a label on the Secret changes nothing;
- keeps the refusal across a restart: it reads `refusedCredentials` back
  from the FrameStorage status before any login.

The hash is readable by anyone who can read FrameStorage. That is why the
procedure uses a long random password: a hash of a guessable one could be
brute-forced.

To unblock:

- **the password in the Secret is wrong**: put the right one in
  (`kubectl -n frame-system create secret generic frame-ceph-dashboard …
  --dry-run=client -o yaml | kubectl apply -f -`). The next read uses it;
- **the account itself is disabled or its counter is high**: on the Ceph
  side, `ceph dashboard ac-user-enable frame-storage-reader`, then set a
  new password (`ceph dashboard ac-user-set-password frame-storage-reader
  -i <file>`) and put it in the Secret. The new content ends the
  refusal, and the first successful login resets Ceph's counter.

## Usable capacity: what the figure is, and what it ignores

The usable capacity of a `ceph-rbd` FrameStorage entry is the cluster's
raw bytes divided by the pool's replicated size. This has two limits:

- The raw figure counts every device class. A pool whose CRUSH rule
  selects some classes only (or, like `hdd2_ssd2`, a fixed number of
  replicas per class) can use part of it. When classes differ in size,
  usable is overstated.
- An erasure-coded pool has no replicated size. Its usable stays
  "unknown" and is not guessed.

Ceph's own per-pool figure is `max_avail`. It takes the rule and the
fullest OSD into account, and the Pools table shows it (from Prometheus).
Read that column before sizing anything.
