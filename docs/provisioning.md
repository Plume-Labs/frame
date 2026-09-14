# How a machine becomes a node

**Decision, 2026-09-10: Talos is out.** The estate moves to a custom
Debian-based image. This document records what that invalidates, what it
turns out never worked in the first place, and what has to be rebuilt.

## What the decision touches, measured

Thirty-four files carry Talos in their name, totalling **4,412 lines**: two
CRDs (`TalosMachineConfig`, `TalosUpgrade`) in both API versions, their
controllers, webhooks, schema tests, RBAC roles, CRD manifests in both
`config/crd/bases` and `charts/frame/files/crds`, two shell scripts
(`gen-talos-iso.sh`, `bootstrap-talos.sh`), and one UI panel
(`TalosOperationsPanel.tsx`).

Another thirty files reference Talos without naming it in their path — most
importantly `internal/controller/frame/framenode_controller.go`, which
imports `github.com/siderolabs/talos/pkg/machinery` and drives discovery and
provisioning through the Talos maintenance API on port 50000.

## What the cluster says, and it is not what the code implies

Before deciding how much of this to rebuild, it is worth knowing how much of
it ever ran. On the live cluster, 2026-09-10:

- Three `FrameNode` objects, 48 days old, all `Ready=False` with reason
  `Discovered` and the message *"Discovery complete; waiting for spec"*.
  Their `.status` carries exactly two keys: `conditions` and
  `observedGeneration`. No `discoveredDisks`, no `discoveredTalosVersion`, no
  `nodeName`, no capacity.
- **Zero** `TalosMachineConfig`. **Zero** `TalosUpgrade`.
- The nodes run Ubuntu under k3s. The only Frame-ish labels they carry are
  `topology.frame.io/{rack,hypervisor,host-pcpu,host-pmem-gib}`, written by
  `deploy/scripts/label-racks.sh` — a shell script talking to Proxmox.

So the Talos path has never produced a single byte of status on this cluster.
Dropping it removes nothing that worked. What the decision does is make a
permanent state explicit.

## The defect this turned up

`framenode_controller.go` projects five labels onto the Kubernetes node —
`frame.plume-labs.io/{rack,service-class,role,rdma}` and
`topology.kubernetes.io/zone` — but it does so in `reconcileOnline`, which is
only reached **after provisioning**. A `FrameNode` with no `spec.disk` stops
at discovery and never gets there. All three stored objects are in that
state, so **no node on this cluster carries any of those five labels.**

`internal/services/provider/inference/inference.go:47` says, in a comment,
that `frame.plume-labs.io/service-class` is "the label the FrameNode
controller already writes on every node", and line 544 uses it as the
`NodeSelector` of every Deployment the inference provider creates. That
assertion is false here. Any pod the provider places would be unschedulable.

It has not bitten yet, and the reason is luck rather than design: the one
`FrameService` on the cluster — `llama-8b` in `inference`, `Ready=False` for
22 days — is blocked one step earlier, at `ModelCacheMissing`, because its
PVC does not exist. Creating that PVC is the obvious next action for anyone
trying to make it work, and it moves the failure to an unschedulable
Deployment with a selector nothing satisfies.

This is not a consequence of dropping Talos. It is a consequence of tying
classification to provisioning, which dropping Talos is what forced us to
look at.

## The re-framing

`FrameNode` has two halves, and only one of them dies.

**Classification survives and matters.** Role, zone, rack, service class and
RDMA are facts about a machine that hold no matter how the machine was
installed. They are what the inference provider, the scheduler and the
placement screen read. This half should not depend on provisioning at all: a
`FrameNode` that describes a node which already exists — however it got
there — should project its labels onto it.

**Provisioning dies.** Maintenance-mode discovery, machine-config generation
and bootstrap are Talos-shaped end to end. A Debian image replaces them with
something structurally different: PXE or a written image, a preseed or
cloud-init, and a join step. That is a design of its own, not an edit to this
one.

### Three pieces of work, in the order they should happen

1. **Decouple classification from provisioning. Done, 2026-09-12.**
   `Reconcile` now looks for the Kubernetes node this object describes before
   it branches on phase or `spec.disk`; if the node is in the cluster its
   labels are projected, however the machine was installed. The latent defect
   above is closed in code.

   **Not yet deployed.** The three `FrameNode` objects will not carry
   `service-class` onto their nodes until the manager running this is rolled
   out.

   One claim in the section above was wrong and is corrected here: the
   discovery path was described as retrying every 30 seconds. It does not. A
   `FrameNode` already at `Discovered` returns without requeueing, which is
   why those three objects settled and stayed quiet. The 30-second loop lives
   in the provisioning path, which they never reached.

2. **Design provisioning on a Debian image. Done — designed and implemented.**
   `docs/superpowers/specs/2026-09-11-provisioning-debian-design.md` is the
   design; `docs/superpowers/plans/2026-09-11-provisioning-debian.md` is the
   plan that built it, in fourteen tasks: preseed rendering and partman
   layouts, ISO remastering, an SSH-based join, the `FrameInstall` CRD and its
   controller, `frame-provisiond` (which builds and serves the images and
   their preseeds), and `frame bootstrap` for node zero when no cluster
   exists yet. See [deployment.md](deployment.md#provisioning-a-machine-with-debian)
   for the runbook and the claims this lot has not yet proven against real
   hardware.

3. **Retire the Talos CRDs.** Deliberately last. `v1beta1` is frozen and
   removing a served kind is a breaking API change; the kinds also appear in
   `charts/frame/files/crds`, `hack/migrate-storage-version.sh`, the webhook
   configurations and the RBAC roles. Nothing is gained by rushing it, and
   the CRDs are inert — zero objects exist. Leaving them in place costs
   nothing but the reading.

## What this does to the programme

The **updates lot** was scoped on the assumption that `TalosUpgrade` was the
mechanism. That assumption is void. Updating a Debian estate is package and
kernel updates plus an ordered drain — a different problem with a different
risk profile, and it needs its own design before it is planned.

The **hardware lot** (`docs/superpowers/specs/2026-09-10-lot1-hardware-redfish-design.md`)
is unaffected. It talks to the BMC, which does not care what the machine
boots. Its `nodeRef` names a Kubernetes node rather than a `FrameNode`
precisely because of this.

## What is not decided here

Whether Frame owns image building at all, whether provisioning is PXE or
written media, and what replaces `TalosUpgrade`. This document scopes the
problem; it does not answer it.

## Reading a stuck `Installing`

`Installing` runs for up to twenty minutes and ends only when the machine
answers on SSH carrying this installation's UID. While it runs, the
`InstallerResponding` condition says whether the installer is speaking:

    kubectl get frameinstall <name> -o jsonpath='{.status.conditions[?(@.type=="InstallerResponding")]}'

| condition | last checkpoint in the message | what it means |
|---|---|---|
| `True` / `Heartbeat` | not advancing between reads | the installer is alive and waiting on a debconf question — go look at the console |
| `False` / `Rebooting` | `late`, lost recently | **the normal tail of a successful install**: `late` is the last thing the installer sends before `finish-install` reboots the machine, and `WaitForOurSystem` then waits out a full POST and boot — past `beaconLostAfter` on every install. Not a fault. |
| `False` / `HeartbeatLost` | `partman` | it died during partitioning or `pkgsel` |
| `False` / `HeartbeatLost` | `early` | it died between the disk-size guard and partitioning |
| `False` / `HeartbeatLost` | `netcfg-done` | **the machine refused**: a named disk was not the size the `FrameInstall` declared, and `preseed/early_command` powered it off. Check `spec.layout.disks[].sizeBytes` against the machine. |
| `False` / `HeartbeatLost` | `netcfg` (no `netcfg-done`) | the `preseed/run` script started but the netcfg re-run never brought the interface up on its static address, so nothing past this ever ran |
| `False` / `NeverSeen` | none | it never booted the media, or the network never came up |
| `Unknown` / `Unavailable` | — | Frame could not ask `frame-provisiond`; `frame-provisiond` restarted and lost its beacon history for an installation it had already reported on; or two `FrameInstall` objects name the same `machineRef` and this one lost the race for it. **Not** a manager restart mid-install -- that case fails closed (`Failed`, via `failRestarted`) before this condition is ever written again. Says nothing about the machine. |

The checkpoint in the message is the furthest the installation has reached,
not the checkpoint of the most recent beacon: the machine re-sends `early`
every fifteen seconds while it works, so a last-write-wins reading of it
would make every stopped-mid-install case look identical. Liveness comes
from how long ago a beacon last arrived; progress comes from the checkpoint;
the two move independently, and both are in the message.

This condition is diagnosis only. It never ends the phase, never fails the
install and never shortens the twenty-minute budget: the phase still ends
only when the machine proves it is ours.
