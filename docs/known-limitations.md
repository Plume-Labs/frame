# Known limitations

Hearthrack is a pre-release. This page says plainly what has **not** been proven, so
nobody finds out the hard way. It is derived from [roadmap.md](roadmap.md),
[SECURITY.md](../SECURITY.md), [getting-started.md](getting-started.md) and the
code; where they disagree, they win and this page is stale (please open an
issue).

## Not yet proven on a real cluster

- **The console terminal** (interactive shell) has never been opened end to end
  against a cluster. Treat it as experimental.
- **The `v1alpha1` → `v1beta1` storage migration** (`hack/migrate-storage-version.sh`)
  has been rehearsed on a throwaway Kind cluster only, never on a long-lived one.

## Security caveats

- The **node-tuning agent is root-equivalent** on every node it runs on, and
  Hearthrack holds real cluster authority. Do not expose the console to the Internet.
- Try Hearthrack on a disposable cluster first. Disk-replacement and wipe flows are
  guarded by serial-number re-entry, but they act on real disks.

## Scope and platform

- x86_64 only.
- Ubuntu is the only supported node OS for bare-metal provisioning.
- A single physical location: multi-site federation is out of scope.
- Disk controllers are expected in IT/HBA mode; hardware RAID volumes are not
  managed.
- Gang scheduling is not implemented.
- No application rollback from the console; Ceph reconstruction shows no ETA.

## Not on this page

Anything marked "not done" in [roadmap.md](roadmap.md) is a gap too; this page
only lists the ones a first-time user is most likely to trip over.
