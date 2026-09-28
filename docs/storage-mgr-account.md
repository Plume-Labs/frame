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
- HTTP/1.1 only, set explicitly on the transport. The "not sent" test
  below relies on `WroteRequest`, which is exact in HTTP/1.1; in HTTP/2 it
  fires only after the body, and a cancelled request returns without
  waiting, so a login that did leave could be counted as not sent.

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

Once step 2 is done the dashboard listens on 8443 in HTTPS only: any
`kubectl port-forward` to port 7000, bookmark or ingress pointing at
`http://…:7000` stops working. Forward 8443 instead
(`kubectl -n rook-ceph port-forward svc/rook-ceph-mgr-dashboard 8443`,
then `https://localhost:8443`; the browser will not know the certificate).

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
keeps the last good read with its date.

**RBAC.** The manager's ClusterRole already grants get, create, update and
delete on every Secret (the FrameService controller needs them), so the
account Secret is read with that. This lot adds one rule naming
`frame-ceph-dashboard-state` (get, update): it states what FrameStorage
uses, and it becomes the only one that applies if the broad rule is ever
narrowed. It restricts nothing today. That the manager never creates the
state Secret is a property of its code, not of its RBAC.

### Undoing step 2 (back to plain HTTP)

If TLS breaks the dashboard or something that depends on port 7000,
undo it, in this order:

```sh
kubectl -n frame-system delete secret frame-ceph-dashboard   # the manager stops logging in: NotConfigured
$TOOLS ceph dashboard ac-user-delete frame-storage-reader
$TOOLS ceph dashboard ac-role-delete frame-storage-reader
kubectl -n $NS patch cephcluster rook-ceph --type merge -p '{"spec":{"dashboard":{"ssl":false}}}'
kubectl -n $NS get svc rook-ceph-mgr-dashboard   # port 7000 again
```

Deleting the account Secret first means no login is ever sent in clear
text to port 7000. The certificate left in the mgr configuration is
unused while `ssl` is false (`ceph config-key rm
mgr/dashboard/crt` and `mgr/dashboard/key` remove it).

### Certificate expiry and renewal

The certificate of step 1 is valid 825 days. Its expiry date is in
`dashboard.crt` (`openssl x509 -noout -enddate -in dashboard.crt`) and
in the FrameStorage status (`status.cephPlacement.certificateNotAfter`,
read from `ca.crt` in the account Secret). The Storage screen's
"À traiter" banner warns from 30 days before (warning), then says it has
expired (critical): every read then fails on TLS. Write the date in the
operations calendar when running step 1.

To renew: run step 1 again (new key and certificate, same subjectAltName),
then replace `ca.crt` in the account Secret with the new certificate:

```sh
kubectl -n frame-system create secret generic frame-ceph-dashboard \
  --from-literal=url=https://$SVC:8443 \
  --from-literal=username=frame-storage-reader \
  --from-file=password=frame-mgr.pass \
  --from-file=ca.crt=dashboard.crt \
  --from-literal=serverName=$SVC \
  --dry-run=client -o yaml | kubectl apply -f -
```

`frame-mgr.pass` was shredded in step 5: read the password back from the
Secret first (`kubectl -n frame-system get secret frame-ceph-dashboard
-o jsonpath='{.data.password}' | base64 -d > frame-mgr.pass`, `umask 077`),
and shred it again afterwards. Restart the mgr if the dashboard keeps
serving the old certificate (`$TOOLS ceph mgr fail`). Between the two
steps, reads fail on TLS (the pinned certificate no longer matches): an
`Error`, not a failed login, since the TLS handshake fails before any
password is sent.

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

So the manager never sends a login that the cluster could forget. Before
each login it writes the intention of that login, durably, to a
dedicated Secret, `frame-system/frame-ceph-dashboard-state`
(write-ahead):

1. It reads the Secret and validates it strictly (see below). Then it
   updates it to add the account to `pending`. A write that fails, a
   conflict with another writer for instance, is re-read and retried up
   to 3 times. If it still fails, there is no login.
2. The login is sent only after that update succeeds.
3. After the login:
   - **success**: the account is removed from `pending`. The token stays
     in memory, and there's no new login until it expires. If the removal
     fails, the next login is blocked with "outcome unknown". That is as
     soon as the token expires, in the same process, not only after a
     restart. This is fail-closed, and an operator unblocks it;
   - **refused** (HTTP 400 `invalid_credentials`, or 401): the account
     moves from `pending` to `refused`. If the state Secret was emptied
     during the login, the refusal is written again under a new key. If
     it can't be written at all (the Secret was deleted, say), the status
     says "refusal not recorded, state Secret was reset: do not retry
     before fixing the password";
   - **not sent** (connection refused, DNS, a failed TLS handshake:
     errors that occur before the request is written, detected with
     `httptrace`'s `WroteRequest`): the dashboard never saw the password,
     so the account is removed from `pending` and the next pass tries
     again;
   - **anything else after the request was sent** (read timeout, 5xx,
     connection reset after writing, an unexpected answer): the account
     stays in `pending`, because the outcome is unknown.

An account in `pending` or in `refused` is never tried again. An operator
unblocks it (below). An account is identified by its username and
password, not by the URL: Ceph counts failures per user whatever address
the login came through (a trailing slash, another host name, an IP), so a
refusal blocks the same credentials at every URL. An annotation or a label on the account Secret
changes nothing. A new password is a different account. A pass
also stops at its first failed login: no GET of that pass logs in again.

The state Secret holds:

- `hmac-key`: exactly 32 random bytes. It is generated only when the
  Secret is entirely empty, in the same update as the first intention;
- `key-fp`: the first 8 bytes of SHA-256(`hmac-key`), in hex;
- `pending` and `refused`: one HMAC-SHA256(`hmac-key`, username NUL
  password) per line, in hex, at most 64 each (see Limits).

Builds before this one put the URL in the MAC. Their entries no longer
match anything: they would block nothing and still count against the
limits. The state Secret has never been deployed with them; if one
exists, clear `pending` and `refused` (see Unblocking) before deploying.

The password is never stored, and without the key the values can't be
checked against guesses. The FrameStorage status carries only
`AuthRefused` or an `Error` and its message, never these values.
FrameStorage is readable by every Frame viewer; the state Secret is not.

**Strict validation.** The manager does not log in, and the status says
"state Secret corrupt", in any of these cases:

- a key other than 32 bytes;
- a `key-fp` that doesn't match the key, e.g. a key replaced by someone
  else;
- lists or a fingerprint without a key;
- an entry that isn't a MAC.

**Limits.** `refused` keeps 64 accounts. Past 64 distinct refused
accounts, the oldest entry is forgotten; if that account came back, it
could cost one more failed login.

`pending` is never trimmed. With 64 logins of unknown outcome, the
manager logs in no more, for any account, and the status is an `Error`
"too many unresolved logins, clear pending". Forgetting one of them
would unblock an account that may already have failed. Clear `pending`
(below) once the accounts are checked.

The deployment manifests create `frame-ceph-dashboard-state` empty
(`config/manager/ceph_dashboard_state.yaml`,
`charts/frame/templates/ceph-dashboard-state.yaml`, kept on uninstall).
The manager's RBAC names it: get and update on that one Secret (no
create verb, since `resourceNames` can't restrict a create). The broad
Secret rule described under Steps still applies alongside it.

### A missing state Secret

If `frame-ceph-dashboard-state` doesn't exist, the manager does not log
in, because the intention couldn't be written. The status says `Error`
with "state Secret missing: frame-system/frame-ceph-dashboard-state,
recreate it". The manager never creates it. Recreate it empty:

```sh
kubectl -n frame-system create secret generic frame-ceph-dashboard-state
```

A `helm upgrade` of the chart (`templates/ceph-dashboard-state.yaml`) or a
re-apply of `kustomize build config/default` recreates it too. The next
read logs in.

### Unblocking

- **The password in the account Secret is wrong**: put the right one in
  (`kubectl -n frame-system create secret generic frame-ceph-dashboard …
  --dry-run=client -o yaml | kubectl apply -f -`). It is a different
  account, and the next read tries it.
- **The account is disabled in Ceph, or its counter is high**: on the
  Ceph side, run `ceph dashboard ac-user-enable frame-storage-reader`,
  then set a new password (`ceph dashboard ac-user-set-password
  frame-storage-reader -i <file>`) and put it in the account Secret. The
  first successful login resets Ceph's counter.
- **"too many unresolved logins"**: check the accounts on the Ceph side
  (counter, enabled), then clear `pending` only:

  ```sh
  kubectl -n frame-system patch secret frame-ceph-dashboard-state --type json \
    -p '[{"op":"remove","path":"/data/pending"}]'
  ```
- **Retry the same account deliberately** ("outcome unknown", or after
  re-enabling it in Ceph): clear the lists and keep the key:

  ```sh
  kubectl -n frame-system patch secret frame-ceph-dashboard-state --type json \
    -p '[{"op":"remove","path":"/data/pending"},{"op":"remove","path":"/data/refused"}]'
  ```

  Or empty the Secret completely (remove every key, or delete and
  recreate it at once). A new key is then generated with the next
  intention. Deleting the Secret without recreating it blocks every
  login.

  Do this while no login is in flight, for instance with the manager
  scaled to zero or between two reads. Clearing the Secret races with a
  login in progress. An emptied Secret is handled (the refusal is written
  again), but a deleted one loses the refusal, and the status says so.

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
