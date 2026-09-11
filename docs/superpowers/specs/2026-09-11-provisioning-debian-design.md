# Provisioning a Debian node — design

**Status:** approved 2026-09-11. Supersedes nothing; it answers the question
`docs/provisioning.md` scoped and deliberately left open.

`docs/provisioning.md` established that `FrameNode` has two halves, that only
classification survives, and that provisioning on a Debian image "is a design
of its own, not an edit to this one". This is that design.

## 1. What this builds

A machine with a BMC becomes a Kubernetes node, driven by Frame, without
anyone standing in front of it.

Concretely: Frame builds a per-machine Debian installer image, mounts it on
the machine as virtual media over Redfish, boots it, waits for the installed
system to answer, joins it to a cluster over SSH, and reports a `Ready` node.

### In scope

- A new kind, `FrameInstall`, and its controller.
- `internal/provision`, a package that performs an installation and **does not
  import Kubernetes**.
- `frame bootstrap`, a command-line consumer of that package for the case
  where no cluster exists yet.
- A screen listing installations and their phase, and creating one from an
  already-inventoried machine.

### Out of scope

- Classification. Projecting `role`, `rack`, `service-class`, `rdma` and zone
  onto a node is work item 1 of `docs/provisioning.md` and stays separate.
  `FrameInstall` ends at a `Ready` node, plus the kubeconfig Secret of §6
  when it created the cluster.
- Writing `FrameMachine.spec.nodeRef`. It lives on `spec`, so it is a fact an
  operator declares, not one a controller writes. `FrameInstall` leaves it
  alone; the screen may offer to fill it, which is a human action.
- Migrating workloads, or moving Frame itself, onto a newly built cluster.
- Retiring the Talos CRDs (work item 3 of `docs/provisioning.md`, deliberately
  last).
- Updating an installed estate. That assumption died with `TalosUpgrade` and
  needs its own design.
- Building an opinionated node image. See decision 4.

## 2. The four decisions

Each was taken explicitly, with its alternative on the table.

**Decision 1 — Frame drives the whole installation.** Not "Frame holds the
recipe and a human runs it", not "Frame triggers and looks away". The
controller runs the sequence, observes it, and reports failure with the phase
that failed.

*Consequence accepted at the time of the decision:* this cannot be tried until
the preseed itself is proven on a real machine. That is a sequencing
constraint on the plan, not an objection to the design.

**Decision 2 — a remastered ISO over virtual media, not PXE.** The deciding
fact is that virtual media offers no way to pass kernel command-line
arguments: a stock Debian netinst boots to its own menu and waits for a
keypress. Unattended installation therefore requires either a remastered image
or a network boot. PXE was rejected because it requires control of the LAN's
DHCP `next-server` — an infrastructure dependency outside Frame, on the same
network as everything else in the house.

**Decision 3 — no secret ever travels in the installer image.** Frame joins
the machine itself, afterwards, over SSH. The image carries only an SSH
*public* key, which is not a secret.

The alternative — serving the k3s join token to the booting machine — was
rejected on evidence rather than principle. Frame already has a recorded
defect of exactly this shape (an unauthenticated in-cluster path reachable by
notebooks and coding sandboxes). An in-cluster HTTP endpoint that serves a
cluster-membership token would be reachable by every notebook and every
sandbox on the platform.

**Decision 4 — the installed system is plain Debian plus k3s.** Tuning
(logind drop-ins, KSM, kernel parameters) stays the business of the
NodeTuning operator, which exists and runs. The decisive argument is that a
node Frame installed and a node installed by hand then converge to the same
state, so the estate does not split into two generations of which only one is
described.

## 3. Objects

Three kinds describe one machine, each with one job:

| Kind | Answers |
|---|---|
| `FrameMachine` (lot 1) | what the hardware is, and how to reach its BMC |
| `FrameInstall` (this lot) | an installation being attempted, and how it went |
| `FrameNode` | how the machine is classified |

`FrameInstall` was made a separate kind rather than a field on `FrameMachine`
for a measurable reason: `FrameMachine`'s controller is a polling loop that
rewrites sensor status every interval, while an installation is a
twenty-minute sequence of phases that fail independently. Sharing an object
means the two contend on `status` writes. Lot 1 deliberately modelled power
actions as timestamps rather than states *because they are instantaneous*; an
installation is not.

Making an installation its own object also means a reinstallation is a second
object, and the outcome of the first stays readable.

Reviving `FrameNode`'s provisioning half — it already carries disk, network
and hostname fields — was rejected: it re-creates the exact defect
`docs/provisioning.md` found, where label projection is only reachable by
provisioning.

`FrameInstall` is `v1beta1`-only with no conversion webhook, following the
`FrameTask` and `FrameMachine` precedent. `v1beta1` is frozen for existing
kinds; a new kind ships into it without a webhook.

### Shape

`spec` carries:

- `machineRef` — the `FrameMachine` supplying BMC address and credentials.
- `hostname`, and a static network configuration (the shape `FrameNode`'s
  `NetworkSpec` already defines: address, gateway, DNS, VLAN, bond).
- `layout` — `single-disk` (exactly one disk) or `mirror` (exactly two, as
  Linux software RAID 1), each naming its disks by `/dev/disk/by-id` (see §5),
  plus an escape hatch for a raw partman recipe.
- `cluster` — `join` (this cluster) or `init` (a new one). See §6.
- `confirmSerial` — the machine's serial, retyped by hand. See §8.

`status` carries the phase, the phase's start time, the pinned SSH host key,
the resulting node name, and on failure the phase that failed and why.

## 4. Lifecycle

Every transition is tied to a signal that can be read. The phase that is
long and nearly blind is named as such rather than dressed up.

| Phase | What ends it |
|---|---|
| `Pending` | validation passes, including the destructive guard (§8) |
| `Preparing` | the image is built and its URL answers |
| `MediaAttached` | `InsertVirtualMedia`, then **re-reading** `VirtualMedia/2.Inserted: true`; boot override set to CD, once, with an explicit mode |
| `Installing` | SSH answers at the target address, our key is accepted, **and** a marker file carries this `FrameInstall`'s UID |
| `Installed` | media ejected, boot override cleared |
| `Joining` | the k3s step returned |
| `Ready` | a node of the expected name exists in the target cluster and is `Ready` |

**The UID marker is load-bearing.** Without it, "SSH answers at
192.168.2.x" is satisfied by *any machine already at that address* —
including the one we believed we were overwriting and in fact never touched.
The preseed writes the marker; the UID is baked into the image at build time,
so it proves this system came out of this installation.

Between boot and the first SSH answer, Redfish offers only `PowerState` and
`Oem.Hp.PostState`. Lot 1 established that this BMC replays cached sensor
readings as live ones in that window, so nothing else there is trusted. The
phase has a timeout and its failure names itself.

Each phase carries its own timeout.

## 5. The installer image

**Built per machine, not generic.** Because no secret enters it (decision 3),
there is no reason to separate a generic image from a configuration fetched
during installation — which removes an entire MAC-based identification
protocol. Hostname, static network, disk recipe, SSH public key and the UID
marker are baked in. The image *is* the installation intent, and it is
readable in full.

Build: unpack the Debian netinst, inject `preseed.cfg`, rewrite the boot
configuration to `auto=true priority=critical`, repack with `xorriso`
preserving the hybrid boot record. One function, two callers — a Job in
cluster, or the command directly.

The image must be fetched by the BMC over HTTP, so the endpoint serving it is
reachable on the management network, not only on the pod network. It carries
no secret, but its path is unguessable so that a scan cannot enumerate machine
configurations.

**Two details that cost a day each if discovered in flight:**

*Boot mode* is set explicitly through Redfish (`BootSourceOverrideMode`),
UEFI or legacy, never inherited from whatever the machine had. An image that
boots in the other mode says nothing useful; it stays on a black screen.

*Disks are named by `/dev/disk/by-id`, never `/dev/sda`.* The ML350 Gen9
carries eight disks and its IML entry `1832` warns that residual logical-volume
metadata may hide some of them from the host. If a named disk is absent the
recipe **fails loudly** — it never falls through to the next disk. This is the
direct link to §8.

## 6. Cluster target, and the cold start

`spec.cluster` says which cluster the node joins: Frame's own, or a new one.

**Creating a cluster is simpler than joining one.** `k3s server
--cluster-init` needs no token. Joining does, and that token lives at
`/var/lib/rancher/k3s/server/node-token` on a server node, which a pod does
not read. So `join` requires an administrator to have placed the token in a
Secret beforehand; `init` requires nothing.

When `cluster: init`, `Ready` cannot be observed on Frame's own node list. The
controller checks the target cluster — which produces the deliverable that
makes a new cluster usable at all: after `--cluster-init`, Frame reads
`/etc/rancher/k3s/k3s.yaml` over SSH, rewrites the server address to the
node's real address, and stores it in a Secret. Without that the cluster
exists and nobody can talk to it.

**The true cold start — no cluster anywhere — is not solvable by an operator
that runs inside the cluster it is creating.** No tool solves it: `kubeadm`,
`talosctl` and `k3sup` all have a binary run from elsewhere for node zero,
after which the tool moves into the cluster it just created.

So `frame bootstrap` is a command run from a laptop: it talks to the BMC,
installs node zero, runs `k3s server --cluster-init`, and hands back a
kubeconfig. After that Frame runs in the cluster and the command is never
needed again — except on the day that cluster dies, which on a single-node
cluster is a day that exists.

**This is why the package boundary is a requirement and not a preference.**
All installation logic — Redfish client, image build, SSH, join, kubeconfig
extraction — lives in `internal/provision`, which does not import Kubernetes.
The `FrameInstall` controller and `frame bootstrap` are two thin consumers.
This is the shape `internal/redfish` already holds in lot 1: a pure client
with no controller-runtime imports.

Shipping both consumers in this lot is deliberate. A package with a single
consumer drifts: nothing turns red the day someone imports controller-runtime
into it, and the command then costs far more than advertised.

## 7. Credentials

Frame holds an SSH private key in a Secret. Its public half is baked into
every image. The preseed creates a `frame` account with that key, `sudo`
without a password, and no password login at all.

k3s is installed at a pinned version fetched from the internet. That is a
dependency of this design and is stated rather than hidden.

**A limitation named rather than papered over:** Frame cannot know the host
key in advance. Baking it into the image would make the image a secret
carrier, which decision 3 forbids. So it is trust-on-first-use, then pinned
into `status`. What makes this proportionate is the UID marker: an impostor at
that address would have to know a UID that never existed anywhere but inside
that image.

## 8. The destructive guard

The worst outcome of this lot is overwriting the right hardware at the wrong
moment. Four layers, and none of them is a `force` flag:

1. **Disks are named, never guessed.** A `FrameInstall` that does not name its
   disks by `by-id` does not install.
2. **The serial is confirmed by hand.** `spec.confirmSerial` must equal what
   Redfish reports at `Systems/1.SerialNumber` for the referenced
   `FrameMachine`. A typo is a refusal, not an installation somewhere else.
   This catches "I pointed at the wrong machine".
3. **A live cluster member is refused.** If a node carries this name or this
   address and is `Ready`, the request is refused outright. Reinstalling a
   live node requires removing it from the cluster first, explicitly.
4. **The installer checks again on the machine.** The preseed refuses if the
   named disk is not the expected size. Frame checked remotely; the machine
   checks locally. Both can be wrong, but not in the same way.

## 9. Permissions

Creation is `admin`-tier only. `viewer` reads, to follow progress.

Lot 1 shipped exactly this defect once: labelling the editor role gave the
editor tier power control, a one-request escalation that took a commit. So
`frameinstall_editor_role.yaml` carries **no tier label**, with the comment
saying why — the precedent `frameuser_editor_role.yaml` already carries that
comment for the same reason.

Creation and terminal outcome are recorded as `FrameTask` audit entries.
Writes made directly with `kubectl`, bypassing the proxy, are not captured —
which is true of every kind and is stated here so nobody assumes otherwise.

## 10. Failure

Each phase times out; on expiry the object goes `Failed` naming the phase.

Frame ejects the media and clears the boot override exactly once: on leaving
`Installing` successfully, or on entering `Failed`. Both paths must do it —
otherwise a machine that failed mid-install reboots into the installer
forever. The override is set as one-shot, which is a second belt rather than
the only one.

**No automatic retry.** Replaying a destructive operation without a human
asking is a fault. A failure is retried by creating a new object.

A finalizer ejects media if the object is deleted mid-flight; without it,
deleting the object strands the hardware in that state.

## 11. Testing

`internal/provision` is Kubernetes-free and tests against a fake Redfish
server. Lot 1 already provides the harness and 42 real captures of the
ML350 Gen9, including its degraded states.

The state machine is tested transition by transition, and the question that
decides each test is the same one: would a broken version pass? A version that
declares `Installing` complete without reading the UID marker must turn a test
red.

On the image, the check that matters is not that it boots — it is that **no
secret material is in it**. The built image is searched for the private key.
That test fails against the naive version of this design.

**Three things cannot be proven without hardware:** that the image boots, that
partman partitions, and that k3s joins. They are written as unexecuted until
the installation runs on the ML350 Gen9 — the same treatment as lot 1's
browser check, which still carries that label in `docs/deployment.md`.

## 12. Screen

A list of installations with their phase, and creation from an
already-inventoried machine. It does not compose partman recipes with a mouse.

## 13. Assumptions to verify, not assume

- Pods can reach the machine network over SSH. The current nodes are on
  `192.168.2.0/24` and egress appears to work; it is checked, not assumed.
- The BMC can fetch the image URL. This requires the serving endpoint to be
  reachable from the management network.
- The exact Debian netinst version and its SHA256 are pinned by the plan's
  first task, from the published checksum file, and carried in the plan's
  Global Constraints. This document deliberately does not name a version it
  has not verified; naming an unverified one would be worse than deferring it
  to the one step that can check.
