# FrameService type `cache`: how Valkey is run

Date: 2026-11-07. Status: implemented, nothing migrated. Moving an existing
Redis onto it is a separate piece of work.

## Decision

Frame runs Valkey itself, as a single-replica StatefulSet, with the official
image. No operator, no replica, no automatic failover.

## What was compared

Read on 2026-11-07 from each repository. All four are Apache-2.0, so licence
decides nothing.

| | Latest release | What it runs | Failover without a Sentinel-aware client | State |
|---|---|---|---|---|
| [valkey-io/valkey-operator](https://github.com/valkey-io/valkey-operator) | v0.8.0, 2026-10-22 | `ValkeyCluster`, `v1alpha1` | Cluster protocol: the client must speak it | README: "not ready for production use", the API may change |
| [hyperspike/valkey-operator](https://github.com/hyperspike/valkey-operator) | v0.0.61, 2025-10-12 | Cluster mode | Cluster protocol | No release in over a year |
| [SAP/valkey-operator](https://github.com/sap/valkey-operator) | v0.1.20, 2026-11-02 | Primary and replicas, a Helm chart underneath | No: failover is Sentinel, clients must use it | Active, 9 contributors |
| [chideat/valkey-operator](https://github.com/chideat/valkey-operator) | v2.1.0, 2026-09-28 | Sentinel or Cluster | No, same | Active, 6 contributors |

None gives what the brief asked for. Automatic failover behind an unchanged
`redis://` address does not exist in these projects: it takes Sentinel or Cluster
support in the client. Cluster mode also constrains BullMQ, whose keys must then
share a hash tag, so it is a change to every consumer and not just to the server.

An operator would therefore add a CRD, a controller to keep current and a
release cycle to follow, for a topology we would not use.

## Why a StatefulSet

- Over a Deployment: a pod is not replaced until the old one is known gone, which
  is what a volume holding an AOF wants, and the claim comes from
  `volumeClaimTemplates`, so the manager needs no `create` on volumes.
- `podManagementPolicy: Parallel`: with one replica it changes nothing about
  ordering, and it avoids the stuck rollout `OrderedReady` documents.
- The template is compared by a hash of what Frame last wrote, not field by field:
  the apiserver's defaults then cannot read as a difference.

## The ceiling

The server is one pod. A restart is a few seconds plus the AOF replay. A node
that dies takes longer: a StatefulSet pod on an unreachable node is not replaced
until the node is confirmed gone, by design. `appendfsync everysec` loses a second
or two of writes, more on a disk that stalls, and on a slow disk the AOF holds
writes up, as Redis did on Ceph in September; liveness is a TCP check so that a
stall does not get the server killed.

Revisit when the official operator leaves alpha and a consumer needs a replica.
The FrameService contract (`profile`, `maxMemory`, the binding Secret) does not
expose the topology, so it can change underneath.
