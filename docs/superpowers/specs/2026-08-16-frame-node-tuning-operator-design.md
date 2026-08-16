# NodeTuning — low-level node configuration for Frame

**Status:** design, approved 2026-08-16
**Scope:** a `NodeTuning` CRD, a node agent, and a controller in the existing
`frame-controller-manager`. Not a new operator.

## Why

Frame ships six node-tuning artifacts under `deploy/kubernetes/base/`:
`ksm-tuner`, `cpu-manager-policy`, `kmod-rdma-loader`, `nfd`, `gpu-mig-config`,
`nvidia-mps`. **None of them is referenced in `base/kustomization.yaml`**, so
none is part of any deployment path. Each has to be applied by hand, and
nothing reconciles it afterwards.

That is not hypothetical. On 2026-08-16 `ksm-tuner` was found never to have
been deployed at all, and when it was, it turned out not to work: it set
`/sys/kernel/mm/ksm/run=1` and stopped there, which merges nothing, because KSM
only scans memory a process has explicitly marked mergeable. The real fix —
`MemoryKSM=yes` on the systemd unit that owns containerd — needed a file on the
host, a unit restart, and workload recreation, none of which any manifest
expressed or verified.

Three failures made that expensive, and the design exists to prevent each:

1. **Written is not effective.** The drop-in sat on disk with
   `MemoryKSM=no` reported by systemd until the unit was reloaded. A manifest
   that only writes files cannot tell the difference.
2. **Restarting a node from a pod kills the caller.** Bouncing k3s stops the
   kubelet that owns the pod issuing the command.
3. **Applied is not realized.** Only containers created after the restart
   inherit the flag, so a node can be correctly configured and still gain
   almost nothing until its workloads are recreated.

## Scope

The dividing line is what `tuned` can own (see "Delegation to tuned" below).
Frame keeps only what tuned structurally cannot reach, and all of it shares the
pattern "the file is not the effect, and something must restart":

- **KSM** — `MemoryKSM=` drop-in on `k3s.service` / `k3s-agent.service`, plus
  the `/sys/kernel/mm/ksm/*` scanner knobs.
- **cpu-manager policy** — needs a kubelet restart and removal of
  `cpu_manager_state`, which is exactly the "restart plus cleanup" case.

**Sysctls and kernel-module loading are delegated to tuned**, which has
first-class plugins for both. Declaring them here as well would put two writers
on one setting.

**MIG** is declared in the CRD but applied as a node label that the NVIDIA GPU
operator already watches. Frame does not reimplement its logic.

**NFD and nvidia-mps get no controller code.** They are ordinary DaemonSets
with no drift-versus-effective problem; their only defect is not being
deployed. They are fixed by adding them to `base/kustomization.yaml`. Writing a
controller for them would be code that reconciles nothing.

## API

`NodeTuning` is cluster-scoped, in `frame.plume-labs.io/v1beta1` (V1 ships on
`v1beta1`; there is no `v1`).

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: NodeTuning
metadata: { name: workers }
spec:
  nodeSelector:
    matchLabels: { node-role.kubernetes.io/worker: "true" }
  tunedProfile: frame-worker   # OS-level profile; owns sysctls and modules
  ksm:
    enabled: true              # default false — see Security
    pagesToScan: 4000          # default 100
    sleepMillisecs: 200        # default 20
    mergeAcrossNodes: false
  cpuManagerPolicy: static     # none | static
  migProfile: ""               # sets the GPU-operator label; empty = untouched
status:
  observedGeneration: 4
  nodes:
    - name: neura-k3s-w1
      phase: InSync            # InSync|Drifted|RebootPending|Applying|Failed
      appliedGeneration: 4
      realization: Effective   # Pending|Effective|FullyRealized
      observed:
        ksm: { memoryKSM: true, generalProfit: 18679296, pagesSharing: 5528 }
      message: ""
  conditions: [...]
```

Fields are typed rather than a generic `map[string]string`. A bag of arbitrary
keys cannot be validated, defaulted, or — decisively — *observed*: the agent
would have no way to say whether a setting took effect.

Two selectors overlapping on one node is a configuration error, not a merge:
the controller reports `Failed` on that node naming both objects, and changes
nothing. Silently picking a winner would make the effective configuration
unpredictable.

## Components

### Agent (DaemonSet, privileged, hostPID)

Applies node-local state and reports what it observes. It **never restarts a
unit itself** — it schedules a transient systemd timer and exits, because the
restart takes down the kubelet that owns it.

It reports *measured* state, never the state it intended to write. Each setting
declares its own proof:

| setting      | proof of effect                                       |
|--------------|-------------------------------------------------------|
| KSM          | `systemctl show <unit> -p MemoryKSM`, `general_profit` |
| cpu-manager  | policy the kubelet actually reports                    |
| tuned profile| `tuned-adm active` matches, and tuned reports no error |
| MIG          | the GPU operator's own status for the node             |

### Delegation to tuned

`tuned` owns the OS-level performance profile: sysctls, CPU governor, disk
scheduler, transparent hugepages, network tunables, and kernel-module loading,
all of which it has first-class plugins for. `NodeTuning` declares *which*
profile a node runs and never restates its contents.

The split is not stylistic. tuned re-applies its profile at boot and on every
profile change, so a setting written by both tuned and the agent has two
writers and no owner — and the conflict surfaces hours later, at the next
reapply, far from the change that caused it. One writer per setting is the
whole point.

tuned is **not installed** on these nodes (Ubuntu Server does not ship it), so
the agent image carries it and runs it, the way OpenShift's NodeTuningOperator
does. That is a real upstream dependency and a materially larger agent image —
tuned pulls python — bought in exchange for a standard, well-understood profile
format instead of a bespoke one.

Frame keeps what tuned structurally cannot express:

- **`MemoryKSM=` on the k3s unit.** tuned has no model for modifying another
  systemd unit's properties. It does have a `[sysfs]` plugin that would happily
  write `/sys/kernel/mm/ksm/run=1` — which is exactly the half that merges
  nothing. Handing KSM to tuned would look like managing it while managing only
  the inert part, reproducing this design's founding bug behind an extra layer
  of abstraction.
- **cpu-manager policy** — kubelet configuration, not OS configuration.
- **MIG profile** — a Kubernetes-level label the GPU operator consumes.
- **The approval, drain and restart lifecycle**, which is the actual subject.

### Controller (in `frame-controller-manager`)

Diffs desired against observed and drives the lifecycle. Non-disruptive changes
apply immediately. Anything needing a restart moves the node to
`RebootPending` and **stops there**.

## Restart orchestration

Approval is per node and carries the generation it approves:

```
kubectl annotate node neura-k3s-w1 frame.plume-labs.io/tuning-approved=4
```

A generation rather than a boolean, so approving one change never silently
authorizes the next one.

On approval: cordon → drain respecting PodDisruptionBudgets → schedule the
detached restart → wait for effectiveness → uncordon. **One node at a time**,
and it refuses to start on a node while any other node is not `Ready`.

Verification uses the unit's `ActiveEnterTimestamp`, not node readiness.
k3s-agent returns in seconds while a node only reports `NotReady` after roughly
forty seconds of missed lease, so a healthy restart usually never flaps the
node — waiting for it to flap reports failure on success. Node readiness is
still required before the node is uncordoned.

### Realization

`realization` separates two things a single "applied" would conflate:

- **Effective** — the setting is live in the running unit; containers created
  from now on get it.
- **FullyRealized** — every container on the node was created after the change.

For KSM the difference is most of the benefit: a node can report the drop-in
loaded and still deduplicate almost nothing while its long-lived pods predate
the restart. The drain that precedes the restart produces `FullyRealized` for
free, so the two states usually converge — but a node tuned without a drain
will honestly report `Effective` rather than claiming more than it delivers.

## Failure handling

A node that does not come back stops the entire campaign: it stays cordoned,
the failure is reported in its status entry, and no other node is touched. A
partially-tuned cluster is recoverable; a rollout that keeps going through
failures is not.

Every probe in the wait loop tolerates failure. Restarting k3s on the server
node takes the apiserver down for seconds, so the controller's own reads are
*expected* to fail mid-wait; treating that as fatal would abandon the node
mid-operation.

## Testing

- **envtest** covers the controller against simulated nodes with injected
  observed status: waiting for approval, rejecting a stale generation, refusing
  a second node while one is unhealthy, halting on failure, and the
  `Effective` / `FullyRealized` distinction.
- **Kind e2e** covers the agent: apply, observe, and report — including the
  case that motivated this design, a drop-in present on disk while systemd
  still reports the old value, and a tuned profile that is set but whose
  `tuned-adm active` disagrees with the spec.

Note `make test-e2e` is not currently wired into CI, so the e2e suite guards
this only when run by hand until that changes.

## Security

The agent is privileged with `hostPID`, which is host-root-equivalent on every
node it runs on. It is the most sensitive component Frame would ship. Its
scope is bounded by construction: it applies only the typed fields above, and
the unit names it may restart are an allowlist, not a CRD field.

Enabling KSM has a tenant-visible consequence that belongs in the CRD's
documentation, not just in an operator's head: merged pages make writes take a
measurable copy-on-write fault, which lets one container test whether another
holds a given page. On a cluster running notebooks and code sandboxes, that is
a real adjacency. The field stays opt-in and defaults to disabled.
