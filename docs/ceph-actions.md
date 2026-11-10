# Everyday Ceph gestures from the console

Storage → **Ceph actions** sends one reversible Ceph gesture at a time:
mark an OSD in or out, restart it, scrub or deep-scrub it, or set a cluster
flag for a while. None destroys data. It is lot 2 of the storage spec
(gestures part); pools, CRUSH rules, mons and mgrs come later.

The object behind it is a `FrameCephAction`
(`api/frame/v1beta1/framecephaction_types.go`), one per gesture; the
controller is `internal/controller/frame/framecephaction_controller.go`. It
writes to Ceph with the operator account of the OSD replacement
(docs/osd-replacement.md, "The operator account"): no new account, no new
dashboard permission.

## The gestures

| Gesture | Ceph command | Checked first | Undo |
|---|---|---|---|
| Mark in | `osd in N` | the OSD exists | mark out |
| Mark out | `osd out N` | every PG active+clean, every OSD up and in, every pool `min_size <= size - 1`, no blocking flag, no other out or restart in flight or ended less than 10 minutes ago | mark in |
| Restart | rollout restart of `rook-ceph-osd-N` (new pod) | same as out | — (it comes back) |
| Scrub / deep-scrub | `osd scrub N` / `osd deep-scrub N` | the OSD is up | — |
| Flag for a while | `osd set F`, then `osd unset F` at the end | no other active action holds F | release |

The out and restart check is the replacement's stand-in for `ceph osd
ok-to-stop`, which the dashboard does not expose; it is strictly stronger
(docs/osd-replacement.md explains why), and it refuses whenever another
host is degraded. It also refuses:

- while `noup`, `norecover`, `nobackfill`, `norebalance`, `pause`,
  `pauserd` or `pausewr` is set (Ceph would not bring the data back);
- while another out or restart has been sent and is not finished, or
  finished less than 10 minutes ago (the PG states may not show its
  effect yet), so two of them never overlap.

While an OSD replacement is running (any phase but Succeeded, Cancelled or
Failed, Paused included), every gesture but a scrub is refused.

Each gesture is sent **once**: `status.sentAt` is written before it is sent,
and an object with `sentAt` never sends it again. A gesture whose answer is
lost ends `Failed` with "check `ceph osd tree`"; send a new one if needed.
A restart is done when the deployment has rolled and the OSD is up again
(15 minutes at most, else `Failed`).

### Flags

`noout`, `norebalance`, `nobackfill`, `norecover`, `noscrub`,
`nodeep-scrub`, each for 5 minutes to 24 hours (2 hours by default). The
others (`pause`, `noup`, `nodown`, `noin`…) are left to the command line.

- While a flag is held, a banner says so on Storage → Ceph and Storage →
  Ceph actions, with its end and a **Release now** button.
- At the end, or on release, Frame unsets the flag. Unsetting is retried
  until Ceph accepts it.
- A flag that was **already set** when the action started (by hand) is left
  set at the end: it was not Frame's.
- One action holds a flag at a time: a second one for the same flag is
  refused while the first is active.
- Deleting an action that holds a flag unsets it first (a finalizer,
  `frame.plume-labs.io/ceph-action-flag`, keeps the object until then).
- A flag Ceph has not unset at its end shows in red on the banner: Frame
  keeps retrying, and `ceph osd unset F` by hand ends it too.
- The dashboard's `PUT /api/osd/flags` takes the whole list: Frame sends the
  list it just read with only its flag added or removed, so no other flag
  changes. A flag changed by someone else between that read and the write
  is overwritten: Frame reads the list back and names it in the message.

## Permissions

- Console: `framecephactions` are written by the **admin** tier only
  (create, and the release patch); the viewer tier reads them, so the
  banner shows to everyone. Every write leaves a FrameTask with who did it.
- Manager: `get` and `patch` on Deployments (the restart), which it already
  held; `update`/`patch` on framecephactions and `update` on their
  `finalizers` (the flag finalizer).
- Ceph: the operator role already holds `osd` read, update and create;
  scrub and the flags use `update`.

## Not here yet

- Pools (create, size, min_size, CRUSH rule, quota, compression) and CRUSH
  rules: the CephBlockPools and the CephCluster live in the Neura repository,
  synchronised by Argo. A pool changed from the console would be put back at
  the next sync. This waits for the decision on question 6 of the spec
  (Argo `ignoreDifferences` on those fields, or a PR per change).
- Restarting a mon or a mgr, mute of a health check with a duration.
