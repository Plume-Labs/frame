# Replacing an OSD's disk from the console

Storage → **Replace OSD** swaps the disk behind one Ceph OSD for a new one,
end to end, one OSD at a time. It is the procedure written for the R320
swap of 13/11 (drain, purge, physical swap, wipe, declaration to Rook,
backfill), with the same checks, run by the manager and shown step by step.
The command-line procedure stays valid and is the fallback.

The object behind it is a `FrameOSDReplacement`
(`api/frame/v1beta1/frameosdreplacement_types.go`); the controller is
`internal/controller/frame/frameosdreplacement_controller.go`.

## What a person does, and what Frame does

| Step | Who | What happens |
|---|---|---|
| Start | admin | types the OSD number and the serial of its disk, read on the label |
| Checks | Frame | read-only, see below; any refusal pauses with the reason |
| Drain | Frame | `ceph osd out N`, waits until every PG is active+clean, then `safe-to-destroy` |
| **Approval 1** | admin | retypes the old disk's serial: the purge cannot be undone. Only an approval given at this step counts: one written earlier (or left from before a pause) is cleared when the step starts |
| Purge | Frame | deployment `rook-ceph-osd-N` to 0, OSD down, `safe-to-destroy` again, `osd purge` (sent once), deployment deleted |
| Swap | person | removes the disk with that serial, inserts the new one (Non-RAID on a PERC/H310) |
| New disk | Frame | exactly one disk must appear that was not on the host when the checks ran, is not declared in the CephCluster, is unknown to Ceph, whole and free, with 512- or 4096-byte sectors, SMART not failed |
| **Approval 2** | admin | reads the new disk's serial on its label and retypes it: the wipe cannot be undone. The console shows the disk's path, model and size, never its serial, until this approval is given |
| Wipe | the node's agent | `wipefs -a`, 100 MiB of zeros, `sgdisk --zap-all`, on the by-id path, after its own checks |
| Declare | Frame | JSON patch of the CephCluster: `test` that entry *i* of `spec.storage.devices` is the old disk, `replace` its name by the new one. Never an apply: nothing else of the spec is touched |
| New OSD | Frame | waits until the host has as many OSDs up and in as before; restarts the Rook operator once after 10 min; pauses after 20 |
| Backfill | Frame | waits until every PG is active+clean |

After it succeeds, update `k8s/storage/cephcluster.yaml` in the Neura
repository to the new by-id path (the replacement says which entry), and
keep the old disk labelled for a week.

### The checks before anything is touched

- No other replacement is running (a paused one still holds the turn;
  the oldest goes first).
- Ceph is `HEALTH_OK`, or `HEALTH_WARN` with only `BLUESTORE_SLOW_OP_ALERT`
  (osd.0's slow BlueStore operations, known). Anything else pauses.
- Every PG is active+clean, every OSD up and in, no `noout`, `norebalance`,
  `nobackfill`, `norecover`, `noup`, `nodown`, `noin`, `pause`, `pauserd`
  or `pausewr` flag.
- Every pool has `min_size <= size - 1`.
- **ok-to-stop.** The dashboard has no `ok-to-stop` route. The replacement
  uses the three conditions just above instead, which are strictly
  stronger: when every PG is active+clean, each PG has `size` copies on
  `size` different OSDs, so stopping any one OSD leaves it `size - 1 >=
  min_size` copies, and it stays active. That is all `ok-to-stop` asks.
- The serial typed matches the one Ceph reports for the OSD
  (`device_ids` of its metadata). If not, the replacement fails: create a
  new one with the right serial.
- The FrameMachine whose `spec.nodeRef` is the OSD's host reports its disks
  (less than 5 minutes ago), with exactly one disk with that serial.
- That disk's `/dev/disk/by-id/wwn-…` path is in the CephCluster's
  `spec.storage.devices`, once; and so is the disk of every other OSD of the
  host (otherwise a disk Rook did not get from the list could pass for the
  new one). Per-node lists (`spec.storage.nodes`), `useAllDevices`,
  `deviceFilter` and `devicePathFilter` are refused: with them Rook could
  take a new disk on its own.

### Pauses, resume, cancel

A refusal pauses the replacement (phase `Paused`) with its reason. **Resume**
retries the step it paused in; it never sends again a purge whose outcome is
unknown: if the purge was sent and the OSD still exists, check `ceph osd
tree`, purge by hand if needed, then resume. A wipe that failed, or got
no answer, goes back to approval 2 on resume: the serial is retyped, and the
new attempt is a new request id; the agent checks the disk again from
scratch. After a pause with no answer, inspect the disk first: the agent may
have been mid-wipe, and its answer, read by request id, still comes.

A pause before the purge was sent (in the purge step itself) brings the
replacement back to approval 1 on resume: the cluster moved, so the earlier
approval no longer counts.

**Cancel** before the purge marks the OSD in again (and scales its
deployment back to 1 if it was stopped). After the purge it undoes nothing:
the replacement stops and says where things stand (purged, new disk,
wiped — from the agent's answer when it came after the request, "outcome
unknown" when there is none — declared), to finish by hand with the fiche.

## The operator account (to create before the first use)

The replacement changes Ceph through the dashboard's REST API, with a
second account held by the manager only. The console's read-only account
(`frame-storage-reader`, docs/storage-mgr-account.md) is not used for this
and does not change. Never `exec` into the toolbox: that would be a root
shell on Ceph.

What the manager sends with this account is fixed in
`internal/cephmgr/operator.go`, and nothing else:

| Route | Ceph command | Dashboard permission |
|---|---|---|
| `GET /api/health/snapshot` | `status` | any account (pgmap: osd read) |
| `GET /api/osd?limit=-1`, `GET /api/osd/<id>`, `GET /api/osd/flags` | — | osd read |
| `GET /api/osd/safe_to_destroy?ids=<id>` | `osd safe-to-destroy` | osd read |
| `GET /api/pool` | — | pool read |
| `PUT /api/osd/<id>/mark` `{"action":"out"\|"in"}` | `osd out` / `osd in` | osd update |
| `POST /api/osd/<id>/purge` | `osd purge-actual` | osd create |
| `POST /api/osd/<id>/scrub?deep=<bool>` | `osd scrub` / `osd deep-scrub` | osd update |
| `PUT /api/osd/flags` `{"flags":[…]}` | `osd set` / `osd unset` of one flag of a fixed list | osd update |

The last two serve the everyday gestures (docs/ceph-actions.md), with the
same account.

The id is an integer rendered by the code; no string from the cluster or a
CR reaches a URL. The role below holds `read`, `update` and `create` on the
`osd` scope and `read` on `pool`, nothing else. Within the `osd` scope the
role would also allow other OSD routes (reweight, scrub, flags, destroy);
the code does not send them.

The steps reuse the dashboard certificate made for the read account
(`dashboard.crt`, step 1 of docs/storage-mgr-account.md):

```sh
NS=rook-ceph
TOOLS="kubectl -n $NS exec deploy/rook-ceph-tools --"
SVC=rook-ceph-mgr-dashboard.$NS.svc

# 1. The role.
$TOOLS ceph dashboard ac-role-create frame-osd-operator "Frame manager: OSD replacement"
$TOOLS ceph dashboard ac-role-add-scope-perms frame-osd-operator osd read update create
$TOOLS ceph dashboard ac-role-add-scope-perms frame-osd-operator pool read
$TOOLS ceph dashboard ac-role-show frame-osd-operator

# 2. The account, its password read from a file.
umask 077
openssl rand -base64 32 | tr -d '\n' > frame-osd.pass
kubectl -n $NS exec -i deploy/rook-ceph-tools -- sh -c 'umask 077; cat > /tmp/frame-osd.pass' < frame-osd.pass
$TOOLS ceph dashboard ac-user-create frame-osd-operator -i /tmp/frame-osd.pass frame-osd-operator
$TOOLS rm -f /tmp/frame-osd.pass

# 3. The Secret, in the manager's namespace. The CA is the one of the read
#    account's Secret.
kubectl -n frame-system get secret frame-ceph-dashboard -o jsonpath='{.data.ca\.crt}' | base64 -d > dashboard.crt
kubectl -n frame-system create secret generic frame-ceph-operator \
  --from-literal=url=https://$SVC:8443 \
  --from-literal=username=frame-osd-operator \
  --from-file=password=frame-osd.pass \
  --from-file=ca.crt=dashboard.crt \
  --from-literal=serverName=$SVC
shred -u frame-osd.pass
```

Logins of this account go through the same write-ahead gate as the read
account's (`frame-ceph-dashboard-state`, keyed per account): a refused
login, or one whose outcome is unknown, is never retried; unblock it as
described in docs/storage-mgr-account.md.

To remove the capability: `kubectl -n frame-system delete secret
frame-ceph-operator`, then `ceph dashboard ac-user-delete
frame-osd-operator` and `ac-role-delete frame-osd-operator`. A replacement
then waits, saying the account is not usable.

## The wipe, on the node

The manager writes a request on the Node (annotation
`frame.plume-labs.io/disk-wipe-request`: id, by-id path, serial, date).
The node's agent (`internal/agent/diskwipe.go`) wipes only when its own
`lsblk` of the same pass shows exactly one disk at that path, with that
serial, whole and free (no filesystem or other signature, no partition, no
mount, no md or ZFS membership), with no holder in `/sys/block/<disk>/holders`,
and the path resolves to that disk. The request must be less than 5
minutes old (and at most 2 minutes in the future, for clock skew): well
short of the 10 minutes the manager waits for an answer, so that a request
it gave up on cannot be acted on later. The answer is written even if the
request was withdrawn meanwhile: it is the only record of what ran. The manager also
withdraws its request when it pauses at the wipe or is cancelled. Each request id is written to the host
(`/var/lib/frame-agent/disk-wipe-acted`) before the first command runs,
and an id already there is never acted on again (the answer then says a
command may have run). The answer goes back in
`frame.plume-labs.io/disk-wipe-result`, saying whether any command ran.

A new disk with partitions or a signature is not wiped by the console: the
replacement pauses and says to clear it by hand (fiche B3.4), then resume.

## Permissions

- Console: `frameosdreplacements` are written by the **admin** tier only
  (create, and the patches that carry the approvals, cancel and resume);
  the viewer tier reads them. Every write leaves a FrameTask with who did
  it.
- Manager: `patch` on the CephCluster named `rook-ceph` only (the declaration),
  and the Deployment rights it already held (scale and delete
  `rook-ceph-osd-N`, restart `rook-ceph-operator`).
- Agent: no new right; it already patches its own Node.

## Limits

- One disk per OSD (no separate DB/WAL device), devices declared in
  `spec.storage.devices` by `/dev/disk/by-id/wwn-…` or `nvme-…`.
- Disks with 520-byte sectors (taken out of a storage array) need a
  low-level format first; the replacement pauses on them.
- Grown-defect counts are not read: look at SMART in iDRAC before approval 2
  if in doubt. A disk whose SMART self-assessment fails is refused.
- Whoever gave an approval is in the FrameTask trail, not in the object.
- The rule refusing approvals at creation (CEL with `optionalOldSelf`)
  needs Kubernetes 1.30 or later; on an older API server the manager still
  clears any approval written before its step.
