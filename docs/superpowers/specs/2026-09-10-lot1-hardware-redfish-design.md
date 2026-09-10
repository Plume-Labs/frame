# Lot 1 — the physical machines, from the console: design

**Status:** design approved 2026-09-10. **Nothing in this lot has run against a
real BMC.** No HPE iLO was reachable when it was written: a sweep of
192.168.2.0/24 found sixteen live hosts and not one Redfish service root. The
ML350 G9 this lot targets was not racked. Every claim below about iLO4's
behaviour is a claim about the specification and about recorded fixtures, and
is labelled unexecuted until someone powers that machine on.

**Goal:** a person signed in to the Frame console can see what a physical
server is made of, what its sensors read, why it last rebooted, and can power
it on, shut it down or restart it — under their own identity, with the writes
recorded, and without reaching for `ipmitool`.

## Why this lot exists

Frame shows the cluster. Below the cluster is hardware, and the console has
never seen it. `deploy/pxe/README.md` and `deploy/omni/README.md` both state
that a machine without a working BMC cannot be enrolled at all — the
out-of-band path is already a hard dependency of provisioning — yet no line of
Go or TypeScript in this repo has ever spoken to one.

The four second-hand servers this cluster is meant to move onto are enterprise
machines with real out-of-band management: iLO4 on the two HP boxes, iDRAC7 on
the Dell. That management is the only thing that answers when the operating
system does not: it is how a machine gets powered on in the first place, and it
is the only record of a reboot that survives the reboot.

## What is in, and what is deliberately not

**In:** inventory (model, serial, BIOS and BMC firmware, CPUs, memory modules
and their slots, drives, NICs); sensors (temperatures, fans, power supplies,
watts drawn); the system event log; power control (on, graceful shutdown,
force off, force restart), the identification LED, and clearing the event log.

**Not in:** remote console and virtual media. On iLO4 both sit behind the paid
iLO Advanced licence and neither speaks Redfish — they are a separate,
proprietary protocol. Building them would be a large piece of work gated on a
licence this estate probably does not hold.

**Not in:** in-band IPMI over KCS as a fallback path. It was considered at
length and declined; the reasoning is below, because the next reader will have
the same idea.

**Not in:** discovery by scanning. The operator connecting to addresses it
found by sweeping a subnet is both noisy and a wider version of the
server-side request forgery this design spends effort bounding. Machines are
registered, not discovered.

## The path: the operator polls, the console reads a CRD

A controller reconciles a `FrameMachine`, connects to the BMC over Redfish on
an interval, and writes inventory, sensors and the recent event log into
`.status`. The console reads `FrameMachine` through the same `frame-uiproxy`
path it reads everything else: impersonated as the signed-in person, filtered
by the same RBAC tiers, and — for writes — recorded as a `FrameTask` by the
recorder lot 0a shipped. Not one line of new plumbing between the browser and
the apiserver.

Two alternatives were rejected.

**A passthrough route in the proxy** — `/bmc/<machine>/…`, authenticating the
caller and forwarding Redfish — always returns fresh data and imposes no bound
on how much of the event log can be read. It was rejected on the same argument
lot 2's design used to reject a dedicated streaming service: the cost of lots
0a and 0c was paid precisely so that there is exactly one path from the browser
to the apiserver, and a second door duplicates token validation, impersonation
and recording in a copy that will drift. It also makes the proxy a
general-purpose forwarder — an SSRF primitive unless the target is strictly
bound to registered machines — and leaves the screen dead whenever the BMC is
slow or down, which is exactly when someone is looking at it.

**In-band IPMI over KCS**, from the host operating system through
`/dev/ipmi0`, was raised as a fallback for a broken BMC network link. It
cannot do the one thing that matters: a machine that is powered off has no
operating system, so the in-band path is dead in the case the lot exists for.
Its remaining value is narrow — BMC unreachable *and* host alive *and* the
machine already a cluster member — and it is not free: reaching `/dev/ipmi0`
from a pod requires a privileged DaemonSet with host device access, which Pod
Security `baseline` does not permit. Granting it means an exempt namespace
running a privileged workload: reopening the escalation class removed from
this repo on 2026-08-10 and fenced by lot 2.

The estate's OS decision moved to a custom Debian-based image while this
design was being written, which *removes* one objection rather than adding
one: `ipmi_si` and `ipmi_devintf` are stock modules on Debian, so
`/dev/ipmi0` would be there for the taking, where an immutable OS would
have needed a system extension. The decision stands anyway, because the
objection that decides it never depended on the OS — a powered-off machine
has no kernel to load a module into.

It is also not the fast path. KCS is a byte-at-a-time legacy interface;
sweeping every sensor across it takes seconds, where Redfish returns a whole
collection in one GET.

What this design does instead is cheap: the Redfish client sits behind a Go
interface, which the tests need regardless. A second transport, if one is ever
justified, is a new implementation of that interface and touches neither the
CRD nor the controller.

## The object

**`FrameMachine`**, namespaced, `v1beta1` only, with **no conversion webhook**.
That last point is load-bearing and is why this is a new kind rather than
fields on `FrameNode`. `v1beta1` is frozen, and `FrameNode` converts from
`v1alpha1`: every field added to it has to round-trip through a conversion that
cannot represent it. `FrameTask` took the same escape hatch in lot 2 for the
same reason. Short name `fm`. It lives in `default`, alongside the existing
`FrameNode` objects.

### Spec

**`bmc.address`** — the IP of the management port, constrained by CEL
`isIP(self)` with a `MaxLength` to bound the rule's cost, exactly as
`FrameNodeSpec.IP` already does. It is an IP literal and not a hostname on
purpose: the controller connects to this address carrying credentials, which
makes the field a server-side request forgery primitive, and requiring a
literal removes name resolution from that path. Loopback (`127.0.0.0/8`) and
link-local (`169.254.0.0/16`) are rejected — the first is the operator's own
pod, the second is where cloud metadata services live.

Whoever can create a `FrameMachine` can make the operator open a connection to
any address it admits, carrying any Secret in the namespace. That is bounded
by RBAC — creating these objects needs the admin tier — and it is stated here so that
widening who may create them is understood as widening that.

**`bmc.credentialsRef`** — the name of a Secret in the same namespace, holding
`username` and `password`. Only the controller reads it. **No console tier is
granted `secrets`**, which is the standing decision recorded on 2026-09-10 in
`deploy/kubernetes/base/rbac-workload-operator.yaml` and is not reopened here.

**`bmc.tls`** — either `caBundleRef`, or `insecureSkipVerify: true`. There is
no permissive default. An iLO4 ships with a self-signed certificate, so
verification will fail out of the box and the machine will sit `Reachable=false`
with a TLS error until someone chooses. That is the intent: the bypass is a
value written in the spec, visible in `kubectl get -o yaml` and in any review,
rather than a default buried in the client.

**`powerRequest {action, requestedAt}`** — one of `On`, `GracefulShutdown`,
`ForceOff`, `ForceRestart`, `ClearSEL`, `IndicatorLedOn`, `IndicatorLedOff`.
The controller acts only when `requestedAt` is later than
`status.lastPowerActionAt`.

Power is written as an event, not as a converged desired state, and the
difference matters. A controller holding "desired: On" would turn a machine
back on after someone pressed its physical power button — it would fight the
person standing in front of the rack. It also could not express a restart at
all, since the desired state before and after is identical. The timestamp
pattern is already in this codebase: lot 2 restarts a Deployment by writing
`restartedAt`, for the same reason.

**`nodeRef`** — optional, naming the Kubernetes node this chassis carries.
Empty for a machine that is not yet part of the cluster, which is the ML350's
state today.

It names a Kubernetes node and not a `FrameNode` deliberately. `FrameNode`'s
discovery and provisioning flow is built on Talos maintenance mode, and the
estate has since moved to a custom Debian-based image; what that leaves of
`FrameNode` is a question for whichever lot takes on provisioning. A
Kubernetes node name is true regardless of how the machine was installed.

### Status

Inventory; sensors; `powerState`; `indicatorLED`; the **most recent 25 event
log entries** plus a count by severity; `lastProbeAt`; and a `Reachable`
condition whose reason carries the failure — `TLSError`, `AuthFailed`,
`Timeout`, `Unsupported`.

The event log is bounded by `MaxItems` because etcd is not a log store. A
machine that has been up for years can hold thousands of entries; twenty-five
is enough to answer "why did it reboot", which is the question the log is for.
Browsing the full log is not in this lot — the passthrough route that would
allow it is the second door this design rejected.

### The controller

`buildRedfishClient(ctx, kube, namespace, address, ref)` mirrors the existing
`buildTalosClient` in `internal/controller/frame/talos_client.go`: the same
shape, the same Secret handling, returning a client behind an interface so
tests serve recorded JSON from an `httptest.Server`.

Polling is `RequeueAfter: 60s` while the machine answers, degrading to 5
minutes after repeated failures. Both figures already exist in this repo's
controllers; neither is new policy.

**The operator must reach the BMC's address at layer 3.** On a shared LOM port
the BMC sits on the same `/24` as everything else and this is free; on a
separate management VLAN it needs a route. Either is fine and the choice is
about exposure, not about this design — a BMC sharing an L2 segment with
workloads is reachable by those workloads, and iLO4 has a history worth
respecting. NetworkPolicies are disabled on this cluster, so egress works
today; if they are ever enabled, this is the first thing that breaks.

## Screens

A new **Hardware** item in the **Compute** group, beside Nodes: the list of
machines on the left, a detail panel on the right with three tabs —
*Inventory*, *Sensors*, *Event log*. This is the shape lot 2 established for
Workloads, and reusing it costs less than inventing another.

**Every panel shows its own freshness.** `lastProbeAt` is rendered, and when
`Reachable` is false the panel is greyed and says how long ago the reading was
taken. A sensor value with no timestamp is a lie the moment the BMC stops
answering, and this is the screen where someone will be reading temperatures
during an incident.

## Writes

Every action is confirmed by a dialog that names the machine and the
consequence, and carries a human-readable `X-Frame-Action` label.

**The confirmation names what is at stake, not just what is requested.** Not
"Power off?" but "Graceful shutdown of `ml350-g9`, which carries 14 pods
including the Frame operator." When `nodeRef` resolves to a cluster node, the
dialog reads what runs on it before asking. Today the ML350 carries nothing;
the entire point of the estate is that it eventually carries the cluster, and a
console that can switch off its own control plane without saying so is a
console that will do it once.

The `X-Frame-Action` label is **bounded to 200 characters at construction**.
`FrameTaskSpec.Action` caps there, and an overflow does not fail the write — it
silently drops the audit record. Lot 2 found that defect on three call sites
after the fact; this lot applies the lesson at the outset.

## Authorization

The RBAC tiers are `admin`, `editor` and `viewer`, and the aggregated roles
select by the `rbac.frame.plume-labs.io/tier` label. **There is no "operator"
tier** — `deploy/kubernetes/base/rbac-tier-bindings.yaml` says so in a comment
because the confusion is easy: `frame:operators` is a *group*, and it binds to
the **editor** ClusterRole.

| Tier label | Bound group | Gains |
|---|---|---|
| `viewer` | `frame:viewers` | `get`/`list`/`watch` on `framemachines` — inventory, sensors, event log |
| `editor` | `frame:operators` | nothing new |
| `admin` | `frame:admins` | the standard admin verb bundle on `framemachines` — the same `create`/`delete`/`deletecollection`/`get`/`list`/`patch`/`update`/`watch` shape every other Frame kind's admin tier gets, plus its `/status` subresource. `patch` is the verb that matters for the console: it is the one verb that reaches `spec.powerRequest`, i.e. power control. |
| — | — | nobody gets `secrets` |

**Power is admin, not editor**, and the distinction is deliberate against the
decision taken the day before this design. Restarting a Deployment is bounded
by an update strategy; powering off a chassis is bounded by nothing, and the
chassis may be carrying the cluster that hosts the console making the request.
Editor is denied the whole bundle, not just `patch`: `config/rbac/
framemachine_editor_role.yaml` keeps the same `create`/`delete`/`patch`/
`update` rules every other kind's editor role has (they are legitimate
`kubectl` operations for someone who already holds them by other means), but
carries no `rbac.frame.plume-labs.io/tier` label, so `frame-editor` never
aggregates them. Making FrameMachine the one kind an admin cannot fully
manage from `kubectl` would be the anomaly among Frame's CRDs, not the
safeguard — the safeguard is that editor cannot reach `patch` at all.

### Registering a machine is a `kubectl` step

The console does not create `FrameMachine` objects, because creating one is
useless without the Secret beside it, and no tier holds `secrets`. Registration
is a manifest applying the Secret and the `FrameMachine` together, documented
in `docs/deployment.md`.

This is the price of granting `secrets` to nobody, and for an estate of four
machines it is the right price. A registration form in the console would
require either a tier that writes Secrets or a back door in the operator that
holds credentials on the caller's behalf. Both are decisions in their own
right and neither is smuggled in here.

## Testing

- a schema test, matching the `*_v1beta1_schema_test.go` file this repo keeps
  per type, covering the CEL bounds on `bmc.address` — including that loopback
  and link-local are rejected
- the controller against an `httptest.Server` serving **recorded iLO4 JSON**:
  a successful poll, a machine that is powered off, a TLS failure, an auth
  failure, and a response missing the fields iLO4 is known to omit
- the timestamp guard: a `powerRequest` older than `lastPowerActionAt` must not
  act, and one newer must act exactly once
- in `src/lib`, where vitest can reach it: sensor severity thresholds, event-log
  severity mapping, and the staleness calculation

## What will not be proven

**A real iLO4.** Recorded fixtures prove that the parsing is correct about the
fixtures. iLO4 speaks a 2015-era Redfish — older `@odata.type` values, absent
fields, and sensor collections that vary with firmware revision. The first
contact with the real machine will find things no fixture predicted; this lot
ships with that check written down and labelled unexecuted, the way lots 0c and
2 did.

`.tsx` files are executed by no test in this repo — vitest runs
`environment: 'node'` with a `.test.ts`-only include — so the detail panel
carries no coverage.

And the estate itself: the iDRAC7 on the Dell and whatever the unidentified IBM
runs are not covered by anything here. They speak Redfish in principle. Whether
they speak the same Redfish is a question for the machine, not for the design.
