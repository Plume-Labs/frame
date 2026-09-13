# Storage

Lot 3 (2026-09-13) gives Frame two custom types for storage — `FrameStorage`
and `FrameDiskClaim` — plus a disk inventory that lives on `FrameMachine`.
Full design in
[`docs/superpowers/specs/2026-09-13-storage-design.md`](superpowers/specs/2026-09-13-storage-design.md).
This document describes what the branch actually does, not what that design
proposed; two places below are called out explicitly where implementation
took a different path than the plan.

**Nothing in this lot is deployed.** The branch ends at a local merge, like
the lots before it. Every command below assumes `config/default` (or the
Helm chart) has actually been applied — see [deployment.md](deployment.md)
for what that requires and what still isn't done.

---

## The two types

### `FrameStorage` — a declared, non-destructive entry

The equivalent of a line in `/etc/pve/storage.cfg`: it names a
`StorageClass`, says what kinds of content may live there, and is refreshed
by polling (`internal/controller/frame/framestorage_controller.go`, every
two minutes). It creates a `StorageClass` if none exists for the name it
names, and it **never deletes a `PersistentVolume` or a `PersistentVolumeClaim`,
in any branch of the controller or the webhook.** That guarantee is not a
comment — it is the absence of the `delete` verb on both resources anywhere
in the manager's RBAC (`config/rbac/role.yaml` grants only `get`, `list`,
`watch` on `persistentvolumeclaims`, and grants nothing at all on
`persistentvolumes`), kept identical between the kustomize and Helm install
paths by `hack/helm-parity.sh`, which fails the moment one path's RBAC
diverges from the other's.

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameStorage
metadata:
  name: ceph-rbd
spec:
  type: ceph-rbd
  content: [workload, model]
  storageClassName: ceph-rbd
  adoptExisting: true   # required: this class already exists in the park
```

- `spec.type` is one of `ceph-rbd`, `ceph-bucket`, `local-path`. NFS and
  iSCSI are not types Frame knows about — nothing in the park runs either.
- `spec.adoptExisting` must be `true` to name a `StorageClass` that already
  exists. Without it, admission refuses the create outright: a `FrameStorage`
  that silently took ownership of an existing class would delete that class
  — and every volume provisioned against it — the moment the entry itself
  was deleted. An **adopted** entry (`status.adopted: true`) owns nothing
  and deletes nothing when it goes away; only an entry that created its own
  class gets an owner reference on it.
- `status.shared` is derived from `spec.type`, never written by a user:
  `ceph-rbd` and `ceph-bucket` are `true`, `local-path` is `false`. It lives
  in status specifically so nobody can declare a local disk shared.
- `status.phase` is `Ready`, `Degraded`, or `Unknown`. Only `ceph-*` entries
  ever leave `Unknown` — a `local-path` entry has no cluster-wide health to
  report, and claiming `Ready` for it would assert something nothing
  measured. A `ceph-*` entry whose health check itself fails to run also
  stays `Unknown`, with the reason in its `Healthy` condition: not knowing
  is not health, in either direction.
- `status.capacity` reports **usable space first, and raw only beside it,
  never alone**. Usable is computed as raw divided by the StorageClass's
  Ceph pool's replication factor. **When the replication factor cannot be
  resolved, no usable figure is reported at all — the field is left empty
  rather than falling back to raw.** This is the fix for the park's own
  capacity incident: OSDs were sized once on a raw number that replication
  quietly divided by three, and a silent fallback to raw here would
  reproduce exactly that read while looking like a correct answer.

  **The replication lookup rests on an assumption never checked against a
  live cluster:** it reads `spec.pool` off the `StorageClass` and looks up a
  `CephBlockPool` of that name in a hardcoded namespace
  (`readReplicationFactor`, `cmd/main.go`). If a `StorageClass` in this park
  does not carry a `pool` parameter, or names a pool that does not exist by
  that name in that namespace, this returns "unknown" rather than a wrong
  number — but the assumption itself has never been run against
  `rook-ceph`'s actual `CephBlockPool` objects.
- `status.claims` counts every PVC provisioning from this entry's class, and
  how many of those carry `frame.plume-labs.io/usage`. See "Labelling an
  existing PVC" below — the gap this reports is normal, not a fault, on the
  day the webhook first lands.

**The Ceph cluster's own namespace and name are hardcoded**, not read from
any spec: `cephClusterNamespace = "rook-ceph"`, `cephClusterName =
"rook-ceph"` (`cmd/main.go`). A `CephCluster` under any other name or
namespace is invisible to `FrameStorage`'s health and capacity reads.

### `FrameDiskClaim` — a one-shot destructive act

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameDiskClaim
metadata:
  name: g9-wipe-w4722rra
spec:
  machineRef: { name: g9 }
  byIDPath: /dev/disk/by-id/scsi-3600508b1001c...
  serial: W4722RRA
  destination: wipe   # or ceph-osd
```

`status.phase` is `Pending`, `Claiming`, `Ready`, or `Failed` — and once it
reaches `Ready` or `Failed` it is terminal. Re-running the same act means
creating a new object, never editing this one.

**On this build, a `FrameDiskClaim` never reaches a phase at all.** The
controller that drives the destructive work
(`internal/controller/frame/framediskclaim_controller.go`) is wired in
`cmd/main.go` with `unconfiguredWiper`, a stand-in whose every method
returns `"destructive backend not configured: this build ships
FrameDiskClaim's guards only"`. That is deliberate: the real implementation
— the one that actually runs `sgdisk` or `ceph-volume` against a node — is a
physical, human-supervised act against one chosen machine, and shipping it
as part of a manager rollout is exactly the mistake this lot's guards exist
to prevent. **A claim created against this build passes its guards (below),
then requeues forever with that error printed in the manager's log on every
retry. It does not become `Failed`.** Do not expect a clean terminal phase —
look at the manager's log, not `kubectl get framediskclaim`, to see what
happened.

### Disks are not a type

A disk has no CRD of its own. It is observed, not declared, and lives in
`FrameMachine.status`:

- `status.inventory.drives[]` — what the BMC reports over Redfish
  (`DriveInfo`: model, size, health, serial, bay location, status reasons).
- `status.storage.observed[]` — what the node's own kernel reports, written
  by the node agent (`ObservedDisk`: `/dev/disk/by-id` path, serial, size,
  occupancy).
- `status.storage.divergences[]` — every disagreement between the two,
  computed by `internal/storage.Join` and never read from either source
  directly.

See the next section for how to read `divergences[]`.

---

## The five content types

`spec.content` is a list, not a single value — one Ceph pool can legitimately
hold both application data and model weights, and forcing a choice would
just produce a second `FrameStorage` entry on the same pool. The five values:

| Content    | What it designates                                    |
|------------|--------------------------------------------------------|
| `workload` | Application data.                                      |
| `model`    | Model weights and large read-mostly caches.             |
| `backup`   | Backup targets.                                         |
| `artifact` | Objects and registries.                                 |
| `scratch`  | Ephemeral, reconstructible data.                        |

A PVC's declared usage (see below) is checked against its class's `content`
list, not against a single type.

---

## Labelling an existing PVC

Enforcement is opt-in, per object, and nothing forces it:

```bash
kubectl label pvc <name> -n <namespace> frame.plume-labs.io/usage=<workload|model|backup|artifact|scratch>
```

One PVC, one command, one label. **Nothing requires this.** The content-type
webhook (`internal/webhook/core/v1/pvc_webhook.go`) carries an
`objectSelector` on `frame.plume-labs.io/usage` — a PVC that does not carry
the label is never sent to admission at all, so it is never checked, never
refused, and never touched. The nineteen PVCs already in the park carry none
of it today; the day the webhook is installed, nothing happens to any of
them — no refusal, no migration, and no window where the cluster cannot
provision a volume because a policy arrived before the labels did. The
webhook's own `failurePolicy` is `Ignore` for the same reason at the other
end: a manager that is down or rolling must not be able to stop the cluster
from provisioning storage.

Once a PVC does carry the label, a mismatch is refused with the entry's name
and its allowed list in the message — for example, labelling a PVC
`usage: model` on a class whose `FrameStorage` entry only lists `[workload]`
is refused, naming both.

`FrameStorage.status.claims` is where the gap between policy and reality is
visible: `{total, labelled}` per entry, read as a fact rather than a fault.

---

## The two disk sources, and how to read a divergence

Frame keeps the BMC's list and the node's own kernel list separate on
purpose — `divergences[]` is the datum, not something to be resolved by
merging the two into one. The worked example below is the one the design
was written against.

**The machine:** an HP ProLiant ML350 Gen9 (iLO 4). The BMC's Redfish tree
lists eight disks. The node's own kernel — via `lsblk` and
`/dev/disk/by-id` — sees seven. The eighth, model `MM1000GFJTE` (a 1 TB
disk), serial `W4722RRA`, bay `2I:6:8`, is masked from the host by residual
logical-unit metadata (HPE IML entry `1832`, severity `OK` — informative,
not a fault flag).

**Read this divergence as `bmc-only`, and `bmc-only` is not a hardware
fault.** The BMC has no field that admits the disk is masked: on this
machine, all eight disks — including `W4722RRA` — report `Health: OK` and
`DiskDriveStatusReasons: ["None"]`. There is nothing to alert on in the
BMC's own view of that disk. `divergences[]` exists precisely because
neither source alone can say what the join between the two says: a disk
present in the BMC's inventory and absent from the kernel's. Do not read a
`bmc-only` entry as "this disk failed" — the operator screens and
`src/lib/storage.ts`'s `describeDivergence` deliberately avoid fault
language for this reason: pulling a healthy drive because a screen implied
it had failed is the exact mistake this section exists to prevent.

The other two reasons:

- `os-only` — the kernel sees a disk the BMC's list does not contain, or one
  whose serial the BMC never reported at all.
- `mismatch` — both sources agree the disk exists, but disagree about it
  (today: reported size).

**The join key is the serial number, and only the serial number.** A
Redfish drive name and a `/dev/disk/by-id` path are two different
namespaces; matching on either would match nothing meaningful. A disk with
an empty serial on either side is never matched — not even against another
empty serial on the other side — because two empty strings compare equal in
Go, and that equality is exactly how a disk would get identified, and
potentially claimed, with no real confirmation behind it.

This example comes from a field log outside this repository, not from a
live run of this code against that machine. **Guard 1 below exists
precisely because that log is not a substitute for reading the serial off
the actual machine before any destructive act.**

---

## Claiming a disk: procedure and the five refusals

1. Read the target disk's serial number and `by-id` path off the machine —
   the BMC's inventory or `lsblk -d -o NAME,SIZE,MODEL,SERIAL` on the node,
   confirmed against the physical bay label, not off a log or a screen from
   an earlier session.
2. Create the `FrameDiskClaim` (see the spec above) with `destination: wipe`
   or `destination: ceph-osd`.
3. Watch `status.phase`. On this build (see above), expect neither — watch
   the manager's log for the "destructive backend not configured" error
   instead, repeating on every retry.

Before any of that ever reaches the (unconfigured) destructive step, the
controller runs five refusals, each closing a real defect found while
building the previous lot:

1. **Serial mismatch or missing.** `spec.serial` must match exactly one
   entry in `FrameMachine.status.storage.observed[]`. An empty serial on
   either side is a refusal, not a match — two empty strings are equal, and
   that is how a disk gets wiped with no real confirmation. **Two disks
   answering to the same non-empty serial is also a refusal**, not a pick of
   either: the claim cannot say which one it means to destroy, and an
   ambiguous match is refused exactly as a missing one is.
2. **A non-`by-id` path.** `spec.byIDPath` must be under
   `/dev/disk/by-id/`. An `sdX` path is refused outright — on the machine
   this was built against, `sdX` ordering changed across all three reboots
   tried.
3. **No report, a stale report, or an occupied disk.** A machine whose agent
   has never reported disks, or reported more than five minutes ago,
   authorises nothing — not knowing is not an authorisation. A disk whose
   `Occupancy` is anything but `free` (mounted, a partition table, an LVM
   PV, an existing Ceph OSD) is refused the same way. **What the operator
   does about it:** check the agent's own health on that node (the report
   age is in the refusal message) or inspect the disk directly with
   `lsblk`/`wipefs -n` before creating a claim on it again.
4. **An ambiguous or unconfirmed prior claim on the same disk.** The claim
   marker lives on the machine itself, not in the controller's memory — a
   manager restart previously replayed an entire destructive sequence
   because the only record it had already run was process-local. Reading
   that marker back can land in one of two states, both refusals if the
   claim isn't the same one already recorded:
   - A different claim's marker is already there → refused, naming the
     existing claim.
   - **This claim's own marker exists but was never confirmed complete** —
     the manager may have restarted mid-gesture, or a previous attempt's
     cleanup failed. **What the operator does about it:** the disk must be
     inspected by hand before any retry. Nothing here resumes automatically
     — a claim that started and cannot confirm it finished is treated as
     unsafe, not as a claim to pick back up.
5. **A failure in the destructive call itself.** The phase does not become
   `Ready` because the failure happened after the useful (destructive) work
   already ran; the error from the destructive backend is surfaced and the
   claim requeues, never silently swallowed. (A *separate*, non-terminal
   detail worth knowing: if the destructive work itself succeeds but the
   completion marker fails to write afterward, the claim **does** still
   become `Ready` — that failure says something about bookkeeping, not
   about the disk, and refusing `Ready` there would force every future
   resume to fail closed forever on a disk that was actually fine.)

---

## What is not delivered

- **The real `Wiper`.** This build ships `FrameDiskClaim`'s guards only; the
  implementation that runs `sgdisk`/`ceph-volume` against a real disk is a
  separate, human-supervised step against one chosen machine, and is not
  part of this lot.
- **NFS, iSCSI.** Neither is a `FrameStorage` type. Nothing in the park
  needs either, and a backend nobody runs is a backend nobody tests.
- **LVM, manual partitioning.** Not `FrameDiskClaim` destinations. `wipe`
  and `ceph-osd` are the two the park actually needs; each other option is a
  distinct destructive operation with its own failure modes this lot does
  not take on.
- **Volume resize.**
- **Snapshots and backup.** A distinct lot.
- **The replication-factor lookup, unverified against a live cluster.** It
  assumes every Ceph-backed `StorageClass` carries a `pool` parameter naming
  a `CephBlockPool` that exists by that name in a hardcoded namespace — see
  "The two types" above.
- **The Ceph cluster's namespace and name, hardcoded in `cmd/main.go`** —
  `rook-ceph`/`rook-ceph` — rather than read from any spec.
- **Deployment.** Nothing in this lot has been applied to a cluster; see
  [deployment.md](deployment.md).
