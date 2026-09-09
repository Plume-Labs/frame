# API & SDK

Frame exposes the Kubernetes CRD API directly — there is no intermediate REST server. All reads and writes go through the standard Kubernetes API at `/apis/frame.plume-labs.io/v1beta1/`.

`v1beta1` is the frozen, stored version and what the SDK speaks. `v1alpha1` is still served and deprecated; a request to it gets a `Warning:` header naming what changed for that kind, and the conversion webhook translates it. See [upgrading.md](upgrading.md), "API versions and the migration path".

---

## Authentication

`authd` is the identity provider for the Cluster Control UI. It issues
`frame_session` cookies from a password or a passkey, mints short-lived
bearer tokens from that cookie, and is where an admin invites a second
person. The routes:

| Route | Caller | Answer |
|---|---|---|
| `POST /auth/bootstrap` | one-shot token | 204 + `frame_session` (12h) — creates the first admin, then closes forever. |
| `POST /auth/login/password` | anyone | 204 + `frame_session` (12h). Disabled account: 401. |
| `POST /auth/login/begin` / `finish` | anyone | Usernameless WebAuthn. Disabled account: 401. |
| `POST /auth/token` | session | `{id_token, expires_in}`. Disabled account: 401. |
| `POST /auth/logout` | anyone | 204, clears the cookie. Unauthenticated by design — it reads no session and checks nothing, so it cannot itself 401. |
| `POST /auth/register/begin` / `finish` | session (an ordinary one, or an accepted invitation's enrolment session) | Enrols a key for the session's owner, never for anyone named in the body. |
| `POST /auth/invite` | admin session | `{url}` — an account with no credential, plus a 24h link. Single-use by *enrolment*, not by acceptance: accepting the same unspent link twice is harmless (204 both times); the link only dies once a credential is added. `role` is `operator` or `viewer` only. |
| `POST /auth/invite/accept` | invitation token | 204 + a 15-minute **enrolment session**, sealed under its own purpose. It reaches only the two WebAuthn enrolment routes above — it cannot mint a token and cannot call `/auth/invite`. The invitee holds no ordinary session after enrolling and signs in normally, the same as anyone else. Disabled account: **403** (`this account is disabled`), not the 401 every other route above answers with — deliberately: the caller has already proved a valid, unspent invitation for this exact account, so naming the reason leaks nothing a 401 would have hidden. 410 (`this account already has a credential`) once the account holds any credential — not "this invitation has already been used": the same guard also fires for a password, which need not have come from this invitation. |
| `GET /auth/credentials` | session (`?user=` for an admin) | The enrolled keys, without public-key material. |
| `DELETE /auth/credentials/{id}` | own, or admin | 204; 409 if it would leave a passkey-only account with no way in. |

**Every route in this table is invisible to the Tasks screen.** A `FrameTask`
is written by `frame-uiproxy` for each mutating request it forwards
(`internal/uiproxy/recorder.go`); the console calls these routes directly, so
the proxy never sees them and no `FrameTask` is created. That covers
bootstrap, invite, accept, enrolment and revoke — including the two writes
with the largest privilege consequences the console can produce, creating an
account and removing a credential.

Their audit trail is `authd`'s own log
(`kubectl -n cluster-control logs deploy/cluster-control-auth`), not
`FrameTask`. Worth stating plainly, because the Tasks nav entry reads "every
write made through the UI, and how it ended": someone who believes that will
read an empty Tasks list as evidence that nothing happened. Everything the
Accounts screen writes *through the apiserver* — role and state changes — is
recorded normally; it is only the `/auth/*` routes above that are not.

The `window.__FRAME_TOKEN__` prose below describes the pre-`authd` world,
where a Secret-issued ServiceAccount token was injected directly into the
page. That path predates the routes above and is not how the console
authenticates today; rewriting it is not this lot's job.

### Development (kubectl proxy)

```bash
kubectl proxy --port=8001
```

Vite proxies `/apis/*` → `localhost:8001`. No token needed — kubectl uses your local kubeconfig.

### Production (ServiceAccount token)

Inject the token before mounting the app:

```html
<script>
  window.__FRAME_TOKEN__ = "eyJhbGc..."  <!-- SA token from K8s Secret -->
</script>
```

The SDK reads `window.__FRAME_TOKEN__` and adds `Authorization: Bearer <token>` to every request. The token must have RBAC rights on the Frame CRDs in the target namespace.

---

## TypeScript SDK

`src/lib/frame-sdk.ts` — `FrameClient` is the main entry point. Import it directly from the repo:

```typescript
import { FrameClient } from '@/lib/frame-sdk'

const frame = new FrameClient()
```

### Jobs

```typescript
// List all FrameJobs (all namespaces)
const { items } = await frame.jobs.list()

// Submit a job — creates a FrameJob CR
const job = await frame.jobs.submit({
  name:         'llm-finetune-v4',
  pipeline:     'training',
  serviceClass: 'HIGH',          // HIGH | MEDIUM | LOW  (default LOW)
  priority:     'critical',      // critical | high | medium | low  (default medium)
  namespace:    'neura-prod',    // the namespace the FrameJob is CREATED in
  gpuCount:     8,
  parameters:   { epochs: '10', lr: '0.0001' },
})

// Cancel a job — deletes the FrameJob CR (and the Argo Workflow via finalizer)
await frame.jobs.cancel('llm-finetune-v4', 'neura-prod')

// Suspend / resume
await frame.jobs.suspend('llm-finetune-v4', 'neura-prod')
await frame.jobs.resume('llm-finetune-v4', 'neura-prod')
```

> **`namespace` changed meaning at `v1beta1`.** It used to be copied into
> `FrameJob.spec.namespace`, a separate field naming where the Argo Workflow
> would be created; the CR itself lived somewhere else. That field is gone
> (F5) — the Workflow is created beside its FrameJob — so `namespace` now
> steers `metadata.namespace` and the request URL. Callers that were passing
> a namespace they did not mean will now create the job there.
>
> A FrameJob with no `Ready` condition — one written before the condition
> tracked the lifecycle — maps to `queued`, and so does one whose `Ready`
> reason is `Submitted`. The two jobs currently stored on the test cluster
> are in that state and will read `queued` until a reconcile gives them a
> real condition. The outcome is not recorded anywhere in those objects; see
> [upgrading.md](upgrading.md).

### Nodes

```typescript
// List FrameNodes
const { items } = await frame.nodes.list()

// Discover a new bare-metal node (creates a FrameNode CR)
const { crName } = await frame.nodes.discover('192.168.10.25')

// Poll until phase === 'Discovered', then configure
await frame.nodes.patchSpec(crName, {
  ip:           '192.168.10.25',
  role:         'worker',
  disk:         '/dev/nvme0n1',
  rack:         'rack-01',   // ≤ 63 chars, label-value pattern
  zone:         'zone-a',    // ≤ 63 chars, label-value pattern
  serviceClass: 'HIGH',      // HIGH | MEDIUM | LOW — '' is no longer accepted
})

// Get node status
const status = await frame.nodes.getStatus(crName)
// → { phase, discoveredHostname, discoveredDisks, discoveredNICs, … }
```

> `FrameNodeStatus.phase` is an **SDK-side** field. `v1beta1` has no
> `status.phase`; the SDK reads the `Ready` condition's `reason`, which holds
> the same strings the field used to (`Discovered`, `Provisioning`, `Online`,
> `Degraded`, `Offline`). An empty string means unreconciled, which is what an
> absent phase always meant, so the wizard's polling contract is unchanged.
> `FrameNode.spec.serviceClass` has **no default** — unlike FrameJob (`LOW`)
> and FrameService (`MEDIUM`) — so an unclassified node reads back as `''` and
> renders as *unclassified*. Do not reintroduce a client-side fallback; the
> disagreement between an SDK default and a schema default is exactly what
> the freeze removed.

### Scheduling policies

```typescript
// List SchedulingPolicy CRs
const { items } = await frame.scheduler.listPolicies()

// Create / update a policy
await frame.scheduler.applyPolicy({
  name:       'hpc-critical',
  scheduler:  'volcano',    // volcano | yunikorn | default
  queue:      'hpc',
  priority:   100,
  preemption: true,
})
// SchedulingPolicySpec has no maxGPUs/maxCPUs — resource ceilings live on
// FrameResourceQuota, not the scheduling policy.

// Delete
await frame.scheduler.deletePolicy('hpc-critical')
```

### Resource quotas

```typescript
// List FrameResourceQuota CRs — usedCPU/usedMemory/usedGPUs and namespaces
// come from status.used / status.namespaces, the controller's real
// aggregation across every projected corev1.ResourceQuota. A field the
// controller hasn't measured yet (or that no namespace reported) reads back
// as '0'/0, which is indistinguishable from a measured zero without also
// checking status.observedGeneration and the Ready condition.
const { items } = await frame.resources.listQuotas()

// Set a namespace quota
await frame.resources.setQuota('neura-prod', {
  maxCPU:    '128',
  maxMemory: '512Gi',
  maxGPUs:   16,
})
```

### Health check

```typescript
const health = await frame.health()
// → { status: 'ok' | 'degraded', version: 'v1beta1', uptime: 0 }
```

---

## Raw Kubernetes API

The Frame CRDs are standard Kubernetes resources. You can use `kubectl` directly:

```bash
# List all FrameJobs in a namespace
kubectl get framejobs -n neura-prod

# Inspect a job (shows conditions + events)
kubectl describe framejob llm-finetune-v4 -n neura-prod

# Create from a manifest
kubectl apply -f config/samples/frame_v1beta1_framejob.yaml

# Watch status
kubectl get framejobs -n neura-prod -w

# Delete (triggers finalizer → removes Argo Workflow)
kubectl delete framejob llm-finetune-v4 -n neura-prod
```

---

## CRD API endpoints

Eight are under `frame.plume-labs.io/v1beta1`; `FrameService` is under the
separate `services.plume-labs.io/v1beta1` group (see
[crd-reference.md](crd-reference.md) for why). Both groups also serve a
deprecated `v1alpha1` — except `NodeTuning`, which postdates the freeze and is
`v1beta1` only.

All are namespaced except `NodeTuning`, which is **cluster-scoped**: its URL
has no `/namespaces/<ns>` segment, and a namespaced list will never return it.

| Resource | Group | Plural | Shortname | Scope |
|---|---|---|---|---|
| FrameJob | `frame.plume-labs.io` | `framejobs` | `fj` | namespaced |
| FrameNode | `frame.plume-labs.io` | `framenodes` | `fn` | namespaced |
| SchedulingPolicy | `frame.plume-labs.io` | `schedulingpolicies` | `sp` | namespaced |
| FrameResourceQuota | `frame.plume-labs.io` | `frameresourcequotas` | `frq` | namespaced |
| TalosMachineConfig | `frame.plume-labs.io` | `talosmachineconfigs` | `tmc` | namespaced |
| TalosUpgrade | `frame.plume-labs.io` | `talosupgrades` | `tu` | namespaced |
| FrameUser | `frame.plume-labs.io` | `frameusers` | — | namespaced |
| NodeTuning | `frame.plume-labs.io` | `nodetunings` | — | **cluster** |
| FrameService | `services.plume-labs.io` | `frameservices` | — | namespaced |

Direct API path pattern:

```
/apis/<group>/v1beta1/namespaces/<namespace>/<plural>/<name>
```

Example curl (with kubectl proxy running):

```bash
curl http://localhost:8001/apis/frame.plume-labs.io/v1beta1/namespaces/neura-prod/framejobs
```

---

## RBAC

`config/rbac/` defines three tier `ClusterRole`s **per kind** — 24 in total,
across both API groups — not three global ones:

| Tier | Verbs on `<kind>` | Verbs on `<kind>/status` |
|---|---|---|
| `<kind>-viewer-role` | `get`, `list`, `watch` | `get` |
| `<kind>-editor-role` | viewer + `create`, `update`, `patch`, `delete` | `get` |
| `<kind>-admin-role` | editor + `deletecollection` | `get` |

(kustomize's `namePrefix: frame-` and the chart both render these as
`frame-<kind>-…-role`.) `frameusers` is the exception in both directions: its
viewer and editor tiers have **no** `/status` rule at all, and its admin tier
has `get`, `patch`, `update` there.

The admin tier used to ship `verbs: ['*']` on the resource. It now ships that
explicit eight-verb list, and **the effective permissions of anything bound to
an admin tier are unchanged** — verified against a real apiserver, allow *and*
deny, on all 24 roles — no existing role gained a verb. `'*'` on `framejobs`
never implied `framejobs/status`, because RBAC resource names are literal, so
the `/status` rules were left at `get` rather than widened to match a wildcard
that did not cover them. The three `frameusers` roles are new rather than
widened: they did not exist before, which made the one kind holding credential
material the one kind with no tier.

Nothing in this repository binds any of these roles. They are correct, frozen
and tested tiers; enforcing them against a human requires authd Stages 2 and 3
(see [roadmap.md](roadmap.md)). Today the UI authenticates with a single
ServiceAccount token.

> **`get frameusers` is equivalent to holding every password hash.** Moving
> `passwordHash` from `spec` to `status` in `v1beta1` bought **write**
> protection — and only in company with the FrameUser validating webhook,
> which is what covers the `v1alpha1` spelling of the same field, RBAC having
> no version dimension. It bought no confidentiality: a status subresource
> splits writes, not
> reads, and a plain `GET frameusers` returns the whole object including
> `status`. A viewer with no `/status` rule at all still reads the hash —
> measured, not assumed. The fix is moving the hash into a `Secret`, which is
> post-freeze. Until then, treat `frameuser-viewer-role` as a
> credential-disclosure role and bind it accordingly.

Bind the appropriate role to the ServiceAccount whose token you inject via `window.__FRAME_TOKEN__`.

---

## Field reference

See [crd-reference.md](crd-reference.md) for the full field-level documentation of all eight CRDs across both API groups.
