# Lot 0 — per-user identity and a task record

**Status:** design approved 2026-09-08. Implementation not started.

## Why this exists

Frame's UI is asked to move from watching a cluster to running one, in the
shape ProxmoxVE gives its users. The gap analysis behind that request is
below; this document specifies only the first lot, which everything else
depends on.

The programme, as decomposed:

| Lot | Scope |
|---|---|
| **0a** | Per-user identity + a record of every write (**this document**) |
| **0b** | Apiserver audit log as a second, cluster-wide source (needs the G9, see below) |
| 1 | Host/hardware through Redfish (power, inventory, health, SMART, console, firmware) |
| 2 | Workloads (pod logs, exec, delete, labels/taints, rollout undo) |
| 3 | Storage (local-path PVC lifecycle, real usage, Velero restore and schedules) |
| 4 | Updates (k3s, Debian packages, Frame itself, the pinned NVIDIA driver) |
| 5 | SDN (bond0/LACP, Cilium, NetworkPolicy, ingress, IP pools) |

Lot 0 comes first because Frame already writes to the cluster and nobody
can say who did it. `deploy/docker/nginx.conf` proxies `/api/` and `/apis/`
to an in-pod `kubectl proxy` sidecar authenticating with the pod
ServiceAccount, and that SA is bound to **both** `cluster-control-viewer`
and `cluster-control-operator`. Cordon, drain, job cancel, policy apply,
quota edit and deployment scale therefore already execute anonymously.
Lots 1-5 add power control, container exec and volume deletion to that same
path. Adding them first would be adding them to an unknown caller.

Two assets make this mostly wiring rather than construction:

- **authd is built and deployed and consumed by nothing.** ES256 OIDC
  issuer with JWKS, argon2id passwords, usernameless WebAuthn, a
  `FrameUser` store with a last-admin webhook guard. Its Stage 2
  (apiserver trusts it) and Stage 3 (UI uses it) were never written.
- **24 viewer/editor/admin RBAC tiers exist as manifests**, bound to
  nobody, because the UI authenticates as one ServiceAccount.

## Decisions

**Users are several humans.** Not one admin, not only agents. The RBAC
tiers therefore have to be enforced, and registration and revocation are
real requirements rather than paperwork.

**Impersonation now, native OIDC later.** Two ways to make the apiserver
see a person:

- Configure `--oidc-issuer-url` on the apiserver so it validates authd's
  tokens itself. Fewer moving parts, but it is a k3s server flag (a
  restart, and a flag to keep — the G9 runbook explicitly wants
  configuration in a versioned file), it makes authd a dependency of
  login itself, and it puts a cluster credential in the browser.
- Put a Frame proxy in front of the apiserver: it validates the token and
  impersonates the user. No cluster change, reversible, the browser holds
  only a short-lived token the apiserver never accepts on its own.

We take the proxy. The UI sends a `Bearer` token either way, so migrating
to native OIDC later — in the G9's `config.yaml`, phase 4 of the runbook —
deletes the proxy and changes no UI code.

**Live tracking and an audit trail are different problems.** The proxy is
the natural place to record what the UI does, and it gets progress for
free by referencing objects that already carry state. It cannot see
anything done outside the UI. The apiserver's audit log sees everything
and carries no progress. We build the first now (0a) and the second after
the G9 migration (0b), because the audit policy is another apiserver
restart.

**A new kind after the API freeze, deliberately.** `FrameTask` is added
knowing the roadmap's complaint about NodeTuning breaking the same rule.
The four things NodeTuning was faulted for — no RBAC tiers, chart does not
ship its runtime, no `Ready` condition, no UI/SDK surface — are all part
of this lot's definition of done rather than follow-ups.

## Architecture

```
browser ──/auth/*──────────────► authd        (password / WebAuthn, session cookie)
   │                              │
   │  POST /auth/token ───────────┘  id_token (ES256, 15 min, memory only)
   │
   └──/api/, /apis/ ──► nginx ──► frame-uiproxy ──► apiserver
                                   │  validates JWKS, impersonates
                                   └─ writes FrameTask (pod SA)
```

### Identity flow

1. The user logs in against authd (already implemented, both methods).
   authd sets `frame_session`, an httpOnly cookie, 12h.
2. The UI calls `POST /auth/token` and receives an `id_token` valid 15
   minutes, **in the response body, never as a cookie** — authd already
   does this so the browser keeps it in memory only. It is minted from the
   user's current role, so a demotion takes effect within one token
   lifetime instead of lasting the session.
3. The UI puts it on `window.__FRAME_TOKEN__`, which `authHeaders()` in
   `src/lib/k8s-watch.ts` already reads and today always finds empty.
4. Every `/api/` and `/apis/` request carries it as a `Bearer`.

### frame-uiproxy

Replaces the `kubectl proxy` sidecar on the same port (8001), so
`nginx.conf`'s two `proxy_pass` stanzas do not change. For each request:

1. Validate the JWT: signature against authd's JWKS (cached, refreshed on
   unknown `kid`), `iss`, `aud`, `exp`. Reject with 401 otherwise.
2. **Strip every inbound `Impersonate-*` header.** This is the whole lot.
   Without it the browser picks its own identity and we have built a
   worse hole than the one we are closing. It is the first test written.
3. Set `Impersonate-User: <sub>` (the email) and `Impersonate-Group:
   frame:<admins|operators|viewers>` from the `groups` claim.
   `GroupForRole` in `internal/authd/issuer.go` already emits the
   unprefixed group; the `frame:` prefix is applied here, matching what
   `--oidc-groups-prefix` would do under native OIDC so the RBAC bindings
   survive that migration untouched.
4. Forward with the pod ServiceAccount token.

Impersonated requests do **not** carry `system:authenticated`. Any
ClusterRoleBinding that grants through that group will not apply to Frame
users — which is the safe direction, and is why the tiers must be bound to
the `frame:*` groups explicitly.

### RBAC

The pod SA drops both `cluster-control-viewer` and
`cluster-control-operator`. It keeps:

- `impersonate` on `users`,
- `impersonate` on `groups`, restricted by `resourceNames` to
  `frame:admins`, `frame:operators`, `frame:viewers`,
- `create`, `patch`, `list`, `delete` on `frametasks`.

Not granted: `serviceaccounts`, `uids`, `userextras`.

**Residual risk, accepted and documented:** `impersonate users` cannot be
bounded by `resourceNames`, because email addresses are not enumerable.
A proxy bug that let an attacker choose the impersonated user would grant
whatever that user has. The mitigation is a rule, and it belongs in
`docs/deployment.md` rather than in a comment: **no RBAC binding may ever
name an individual user.** Everything is granted to the three groups.

The 24 existing tiers are bound to the three groups. The names do not line
up on their own — the tiers are `viewer`/`editor`/`admin`, the FrameUser
roles are `viewer`/`operator`/`admin` — so the binding is stated once here
and nowhere else: `frame:viewers` → every `*-viewer-role`, `frame:operators`
→ every `*-editor-role`, `frame:admins` → every `*-admin-role`. Nothing else
changes about the tiers; this lot enforces them, it does not redesign them.

### FrameTask

`frame.plume-labs.io/v1beta1`, namespaced in `frame-system` (the proxy
writes there whatever namespace the proxied request targets — a task is a
record of an action, not a member of the acted-on object's namespace),
written by the proxy on
mutating verbs only (POST, PATCH, PUT, DELETE). Logging reads would
produce thousands of objects an hour for no information.

```yaml
spec:
  user: alice@example.com
  verb: patch
  target: {group: "", kind: Node, namespace: "", name: w2}
  action: "cordon node w2"        # human-readable label from the UI
  ref:                            # optional, object that carries progress
    {group: frame.plume-labs.io, kind: TalosUpgrade, namespace: frame-system, name: u-42}
status:
  phase: Running | Succeeded | Failed
  httpCode: 200
  message: ""
  startedAt: ...
  finishedAt: ...
```

The proxy creates the task before forwarding and closes it with the
upstream status code and duration. **Progress is never copied.** When the
action has an object that already holds its state — FrameJob, TalosUpgrade,
Velero Backup — the task references it and the panel reads that object's
status. This is what lets the lot ship with **no FrameTask controller at
all**.

Retention: the proxy deletes finished tasks older than 7 days, at startup
and hourly. A CronJob or a controller would both be more machinery for the
same effect.

The task is created with the pod SA, not through impersonation.
Otherwise a viewer could not record the trace of their own 403 — which is
exactly the trace worth having.

Tiers: viewer/editor/admin as for every other kind, `list`/`get`/`watch`
for all three; only the proxy SA creates.

### The Tasks screen

A new view listing tasks newest-first, with the author, the action label,
the outcome, and — when `ref` is set — the live status of the referenced
object. Filterable by user and by outcome. Backed by a `frame.tasks`
namespace on the SDK and a watch, like every other live screen.

## Out of scope for 0a

**0b, the audit log.** Policy at `Metadata` level, mutating verbs only,
noisy system accounts excluded; the file lives on the host and is read by
a read-only `hostPath` DaemonSet served behind the same proxy — one pod on
a single-node cluster. It needs an apiserver restart, so it belongs in the
G9's `/etc/rancher/k3s/config.yaml` (runbook phase 4), not as a patch on
the T320 we are about to switch off.

**Lots 1-5.** No new capability is added here. This lot changes who may
use the capabilities that already exist, and leaves a record of it.

## Rollout

Order matters; inverting the first two locks everyone out and the only way
back is the node's kubeconfig.

1. Bootstrap the first admin account through authd's one-shot
   `/auth/bootstrap` secret, **while the old anonymous path still works**.
2. Bind the three groups to the tiers.
3. Deploy the proxy in place of the `kubectl proxy` sidecar and remove the
   SA's two ClusterRoleBindings in the same apply.
4. Set authd's `RP_ID` / `RP_ORIGIN` to the UI's real hostname and
   `OIDC_ISSUER_URL` / `OIDC_CLIENT_ID` to what the proxy validates. They
   are Stage-1 placeholders today (`frame.local`).

Rollback is redeploying the previous kustomization; the accounts created
in step 1 stay valid for the next attempt.

## Testing

Go unit tests on the proxy: rejected with no token, expired token, wrong
`aud`, wrong `iss`, bad signature; **an inbound `Impersonate-User` header
is dropped**; role-to-group mapping.

Kind e2e, the control that discriminates: a `viewer` gets 403 on a cordon,
an `operator` gets 200, and both leave a `FrameTask` carrying the right
`user`. A proxy that impersonated nothing would return 200 for both — a
test that only asserts the operator's 200 would pass against the broken
version.

On the real cluster after deploy: log in, cordon a node as an operator,
attempt the same as a viewer and see the 403, and find both in the Tasks
screen.

## Files

- `cmd/uiproxy/`, `internal/uiproxy/`, `Dockerfile.uiproxy`, Makefile targets
- `api/frame/v1beta1/frametask_types.go`, generated CRD, tier roles
  (`frame.tierRoleCRDs` in `charts/frame/templates/_helpers.tpl`), chart
- `deploy/kubernetes/base/deployment.yaml` (sidecar swap),
  `deploy/kubernetes/base/rbac.yaml` (SA loses two bindings, gains
  impersonate), `deploy/kubernetes/authd/deployment.yaml` (real RP_ID)
- UI: login screen, token refresh, `TasksView`, `frame.tasks` on the SDK
- `docs/deployment.md`: the no-named-user-bindings rule, the rollout order
