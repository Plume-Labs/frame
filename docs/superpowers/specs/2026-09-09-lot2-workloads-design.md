# Lot 2 — operating workloads from the console: design

**Status:** design approved 2026-09-09. Not implemented.

**Goal:** a person signed in to the Frame console can see every workload on the
cluster, read a pod's logs, open a shell in a container, restart a deployment,
scale it, delete a pod, and edit a resource — under their own identity, with the
writes recorded, and without reaching for `kubectl`.

## Why this lot exists

Frame shows the cluster. It does not operate it. Lots 0a and 0c built the thing
that makes operating it safe: every request the console makes carries the
signed-in person's identity to the apiserver, and every write leaves a
`FrameTask` naming who did what. That machinery has, so far, been used for
cordoning a node and changing a queue weight.

The day-to-day work of running this cluster is not that. It is reading a log,
restarting something that wedged, and occasionally opening a shell. All of it
happens in `kubectl` today, which means it happens outside the identity and
outside the record.

## What is in, and what is deliberately not

**In:** a workload tree across every namespace; pod logs; an interactive shell;
rollout restart; scale; delete a pod; edit a resource's YAML.

**Not in:** port-forward, resource creation from the console, log download, log
search across pods, and any batch action over several objects. None of them is
needed to stop using `kubectl` for the daily work, and each is a lot of surface.

## The path: one door, widened

Everything goes through `frame-uiproxy`, on the path it already serves. The
browser opens a WebSocket; the proxy validates the bearer token, strips any
`Impersonate-*` the caller supplied, sets its own from the verified identity,
and relays to the apiserver. Logs and exec are the same request shape as every
other, held open.

A second door — a dedicated streaming service — was considered and rejected. It
would duplicate token validation, impersonation and recording: three
security-bearing mechanisms in two copies, which drift. The cost of lots 0a and
0c was paid precisely so that there is exactly one path from the browser to the
apiserver.

### Three things the proxy needs first

**`statusRecorder` must implement `http.Hijacker`.** It wraps the
`ResponseWriter` so the FrameTask can be closed with the response's status code.
Go's `ReverseProxy` hijacks the connection to perform an HTTP upgrade; a wrapper
that does not expose `Hijack` makes every upgrade fail. This is the same defect
the same type already had with `http.Flusher`, whose fix carries a comment
saying a wrapper without it "turns every watch into a buffered response". It
will recur the next time someone adds a wrapper for a good reason, so it ships
with a test that fails when the method is removed.

**`ObjectRef` gains `subresource`.** `parsePath` currently keeps `pods` and
discards `exec`, so an exec would record as `create pods/<name>` —
indistinguishable from creating a pod. Without this field, the decision to
record exec sessions does not exist in practice. `FrameTask` is post-freeze,
`v1beta1`-only, with no conversion webhook, so an optional field costs nothing.

**A session-shaped task.** The recorder opens a `FrameTask` when the WebSocket is
established and closes it when the socket closes, carrying the duration. A
three-hour shell must be visible as a three-hour shell.

### Logs are not recorded

The recorder ignores reads by design, and a log read is a read. `GET
pods/<name>/log` therefore leaves no `FrameTask`. This is consistent with
everything else in the product, and it is stated here rather than discovered:
reading a pod's logs can mean reading a secret, and no record of that will exist.
The mitigation is the RBAC tier, not the audit trail.

## Screens

A new **Workloads** tab: namespace → controller (Deployment, StatefulSet,
DaemonSet, Job) → pods.

Infrastructure namespaces are collapsed behind a switch, open on the application
namespaces. The list of what counts as infrastructure lives in `src/lib` — it is
presentation, testable, and amendable without touching a component. RBAC remains
the real filter; this only decides what is folded.

A detail panel per pod — status, containers, restart count, node, age — with
three tabs:

**Logs.** Container picker, live follow, and the previous container's logs. That
last one matters more than it looks: when a container has crash-looped, the
cause is in the instance that died, not the one running.

**Terminal.** An interactive shell, admin only. Rendered with `@xterm/xterm` and
`@xterm/addon-fit` — the first dependencies this programme has added, each with
an upper bound as the repo requires. Emulating a TTY by hand (colours, cursor
addressing, resize, `top`) is not work worth redoing.

**YAML.** The resource as stored, editable.

## Writes

Every action is confirmed by a dialog naming the object and the consequence, and
carries a human-readable `X-Frame-Action` label.

**Restart** patches the controller's `restartedAt` annotation rather than
deleting a pod, so the update strategy is respected. Records as "restart
deployment neura-api".

**Scale** patches the `scale` subresource. The confirmation states the current
and target replica counts, and scaling to zero is named as what it is: a stop.

**Delete a pod** reads `ownerReferences` first. Under a controller the dialog
says it will be recreated; on a bare pod it says the deletion is final and
nothing will bring it back. These are two different actions behind one button and
the person clicking must know which one they have.

**Edit YAML** is the one that could have made the audit trail useless, and is
designed so it does not:

- the console computes the **changed field paths** between the object it read and
  the object being written, and puts them in the label: `edit: spec.replicas,
  spec.template…image`. The `FrameTask.spec.action` field caps at 200
  characters, so beyond three paths it counts the remainder. The diff function
  lives in `src/lib` and is tested.
- the write carries the `resourceVersion` that was read, so a concurrent change
  produces a 409 instead of silently overwriting someone else's work.
- if the object carries Argo CD or Helm ownership metadata, the editor says so
  before the edit: the change will be reverted at the next sync. It does not
  forbid the edit — it prevents three hours spent debugging a change that
  quietly disappeared.

## Authorization

The RBAC file requires every rule to be tied to a call site in `src/`; these
follow that discipline.

| Tier | Gains |
|---|---|
| viewer | the reads the tree needs beyond today's: `daemonsets`, `jobs`, `replicasets` |
| operator | `pods/log` (get), `pods` (delete); `deployments`/`statefulsets` patch and `scale` **only in namespaces that enforce Pod Security** — see below |
| admin | `pods/exec` (create), and update/patch on exactly the kinds the Workloads screen shows: `pods`, `deployments`, `statefulsets`, `daemonsets`, `jobs` |

### Restart and scale require Pod Security first

`patch` on Deployments was **removed from this repo on 2026-08-10** and the
removal is documented in `deploy/kubernetes/base/rbac.yaml`: it is "the single
grant that turned the unauthenticated UI into cluster-admin", because patching a
pod template to add `securityContext.privileged: true` and a `hostPath: /` volume
is root on the node, and no namespace on this cluster carries a
`pod-security.kubernetes.io/enforce` label to stop it. RBAC cannot restrict a
patch to one JSON path, so "may set the restartedAt annotation" and "may make the
pod privileged" are the same grant. An `ApplicationsView` Restart button has
returned 403 ever since.

The same comment names the way to earn it back, and this lot takes it: enforce
Pod Security, then grant the patch only where the privileged payload is blocked.

- Application namespaces get `pod-security.kubernetes.io/enforce: baseline`.
  Infrastructure namespaces are **exempt** — Ceph, the node-tuning agent and the
  Talos tooling legitimately need privileged pods, and labelling them would break
  the cluster.
- The grant is a **RoleBinding per enforced namespace**, not a ClusterRoleBinding.
  A cluster-wide grant would hand back the escalation through the exempt
  namespaces, which is the whole hole.
- Restart and scale therefore work on application workloads and return 403 on
  infrastructure ones. That asymmetry is deliberate and the screen says so rather
  than showing a button that fails.

**Labelling order matters, and getting it wrong breaks deploys at the worst
moment.** Enforcing does not evict running pods; it refuses the next admission.
A workload that has always violated the policy therefore keeps running and fails
the next time it restarts — during an incident, not during this change. So each
namespace is labelled `warn` and `audit` first, the violations are read and
resolved, and only then is `enforce` applied. This lot is not finished until that
pass has been run against every namespace it labels.

This also repairs the Restart button that has been broken since August.

**Logs start at operator, not viewer.** A viewer sees state; logs are the most
likely place for a credential to appear in plain text. Widening this later is
easy and narrowing it after people depend on it is not. This is a judgement, not
a constraint — it is one line in the tier role if it should change.

The YAML editor is bounded by that list. "Edit any resource" would mean granting
admin `patch` across the whole cluster, which is a far larger grant than the
screen needs and would not be tied to a call site as the RBAC file requires.
Editing a Secret or a ClusterRole from this screen is out of scope.

**Exec is admin-only**, per the decision that opened this design. The terminal
tab states, where the person opening the session can see it, that the session is
recorded: who, which pod, when, and for how long — but not what was typed.
Recording keystrokes was considered and rejected: it would capture every secret
typed or displayed, making the audit record itself a sensitive store.

## Testing

What is genuinely testable, and what this lot requires:

- the proxy's `Hijacker`, with a test that fails when the method is removed
- the subresource in the record: an exec must produce a `FrameTask` distinguishable from a pod create
- session open and close, with duration
- in `src/lib`, so vitest can reach them: the changed-field-path diff, the
  infrastructure-namespace list, and **the exec protocol's channel
  demultiplexing** — Kubernetes prefixes each frame with a channel byte (stdout,
  stderr, error). That is pure logic and belongs outside the component.

## What will not be proven

`.tsx` files are not executed by any test in this repo (vitest runs
`environment: 'node'` with a `.test.ts`-only include), so the terminal component
carries no coverage. Neither does a real WebSocket against a real apiserver.

An exec's failure modes are physical — a session that will not close, a resize
that leaves the display offset, a `Ctrl-C` that does not reach the process. None
of them will be known until someone opens a shell from the console on the
cluster. This lot ships with that check written down and labelled unexecuted, the
way lot 0c did.

## Carried in from lot 0c's whole-branch review

The Tasks screen reads `frametasks` in the `default` namespace while the recorder
writes them to `frame-system`, so it has shown an empty list since the lot that
shipped it. This lot puts more records into that screen than anything before it,
so the fix belongs at the front of it: a `taskNamespace` configuration field —
type, default, merge, a Settings input, and documentation. A hardcoded literal
would be the second source of truth that the same review rejected elsewhere.
