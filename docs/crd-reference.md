# CRD Reference

Eight CRDs across two API groups, both at `v1alpha1` and all namespaced:
seven in `frame.plume-labs.io` (this page's first seven sections) and
`FrameService` in `services.plume-labs.io` — a separate group so the service
catalog can move without blocking the `frame.plume-labs.io` freeze (see
[roadmap.md](roadmap.md)). Generated CRDs live in `config/crd/bases/`; sample
CRs in `config/samples/`. Each kind has a controller
(`internal/controller/<group>/`) except FrameUser, and a webhook
(`internal/webhook/<group>/v1alpha1/`).

> `v1alpha1` means the schema may change without conversion guarantees. See
> [roadmap.md](roadmap.md) for the path to a stable API.

---

## FrameNode

A bare-metal machine Frame manages. Bridges a physical/Talos node to its
Kubernetes `v1.Node`.

**Spec:** `ip`, `role`, `network` (`address`, `gateway`, `dns[]`, optional
`vlan`, `bond`), `disk`, `rack`, `zone`, `serviceClass`, optional
`rdmaInterface`, `hostname`, `serverClassRef`.

**Status:** `phase`, `conditions[]`, `talosVersion`, `kubeletVersion`,
`lastHeartbeat`, `capacity`, `allocatable`, `nodeName`, `providerID`.

**Controller:** finalizer-guarded; secondary-watches core `v1.Node` and maps it
back to its FrameNode (`nodeToFrameNode`) to keep phase/versions in sync.

---

## FrameJob

A workload submitted to the cluster, realized as an Argo `Workflow`.

**Spec:** `name`, `pipeline`, optional `serviceClass`, `priority`
(critical/high/medium/low), `namespace`, `gpuCount`, `parameters` (map),
`suspended` (bool, default false).

**Status:** `phase` (Pending/Submitted/Running/Suspended/Completed/Failed),
`conditions[]`, `argoWorkflowName`, `startTime`, `completionTime`, `message`.

**Controller:**
- On create: builds an Argo `Workflow` with `spec.suspend`, `priorityClassName`
  (mapped from `priority` → `frame-{priority}`), and arguments `gpu-count` +
  `service-class`; sets `Submitted` condition.
- On update: syncs `spec.suspended` → `Workflow.spec.suspend` via patch;
  derives `phase` from workflow status (Argo `Succeeded` → `Completed`, etc.);
  surfaces `Suspended` when `spec.suspended=true` and workflow isn't terminal.
- Secondary watch on Argo `Workflow` objects (label `frame.plume-labs.io/job` +
  `frame.plume-labs.io/job-namespace`) — reacts to Workflow changes instead of
  polling every 30 s.
- On delete: removes the Workflow via finalizer.

---

## SchedulingPolicy

Queue / priority configuration for the HPC scheduler.

**Spec:** `scheduler` (volcano/yunikorn/…), optional `queueName`,
`priorityClass`, `gangScheduling`, `preemption`, `priorityValue` (int32,
default 0), `queueWeight` (int32, min 1, default 1).

**Status:** `conditions[]`.

**Controller:**
- Reconciles a cluster-scoped `PriorityClass` named by `spec.priorityClass`
  with `value=spec.priorityValue` and `preemptionPolicy` derived from
  `spec.preemption`. Uses two labels (`frame.plume-labs.io/policy-namespace` +
  `frame.plume-labs.io/policy-name`) to track ownership across scope boundaries.
- When `scheduler=volcano`: reconciles a `scheduling.volcano.sh/v1beta1/Queue`
  with `weight` and `reclaimable`.
- When `scheduler=yunikorn`: reconciles a `yunikorn.apache.org/v1alpha1/Queue`
  with `weight` and `preemption.allowPreemptSelf`.
- Missing scheduler CRD → `Ready=False` with reason `QueueCRDMissing` (graceful
  degrade, no retry storm). Missing PriorityClass CRD → hard fail + retry.
- Cleanup on delete via finalizer (removes PriorityClass + queue).

---

## FrameResourceQuota

Per-service-class resource ceiling. The controller projects it as a
`ResourceQuota` named `frame-<serviceclass>` into every namespace labelled
`frame.plume-labs.io/service-class` with the matching value.

**Spec:** `serviceClass`, optional `maxGPUs`, `maxCPU` (Quantity),
`maxMemory` (Quantity), `maxJobs`.

**Status:** `conditions[]`.

**Quota mapping:** `maxGPUs` → `requests.nvidia.com/gpu`, `maxCPU` →
`limits.cpu`, `maxMemory` → `limits.memory`, `maxJobs` →
`count/framejobs.frame.plume-labs.io`. `maxJobs` counts FrameJob objects, not
the pods their workflows fan out to, and completed FrameJobs keep counting
until deleted.

**Not in scope:** scheduler queue limits. `SchedulingPolicy` already reconciles
Volcano/YuniKorn queues; projecting the same ceiling into a queue would make
two resources authoritative for one number.

---

## TalosMachineConfig

Declarative Talos MachineConfig application to a node.

**Spec:** `nodeName`, `talosEndpoint`, `talosSecretRef`, and one of
`configPatch` (inline YAML) or `configPatchRef` (ConfigMap key selector).

**Status:** `conditions[]` (Ready=True reason `Applied`; False reasons:
`PatchResolveFailed`, `ClientBuildFailed`, `ApplyFailed`).

**Controller:** reads TLS credentials from the referenced Secret (keys
`ca`/`ca.crt`, `crt`/`tls.crt`, `key`/`tls.key`), resolves the patch,
calls `ApplyConfiguration` gRPC with `mode=AUTO`, updates condition. Retries
with 30 s backoff on transient failures. Uses a finalizer for clean deletion.

---

## TalosUpgrade

A Talos OS upgrade for a single node.

**Spec:** `nodeName`, `talosEndpoint`, `talosSecretRef`, `image`, optional
`preserveData`.

**Status:** `conditions[]` (Ready=True reasons: `UpgradeRequested`,
`AlreadyAtVersion`; False reasons: `ClientBuildFailed`, `UpgradeFailed`).

**Controller:** generation-based idempotency guard — only calls `Upgrade` gRPC
when the `Ready` condition's `observedGeneration` differs from `.metadata.generation`,
preventing re-trigger while the node is rebooting. Uses `stage=false, force=false`.
Retries with 30 s backoff on transient failures.

---

## FrameUser

A person who can sign in to the Cluster Control UI. Written by admins and by
`authd`; it has **no controller** — nothing reconciles a FrameUser into cluster
state. It is a record that `authd` reads at sign-in, which is why it is the one
CRD with no entry in the controller table.

**Spec:** `email` (becomes the Kubernetes username), `role`
(`admin` | `operator` | `viewer`, decides the group the issued token carries),
`passwordAuth` (`enabled` | `disabled`, **defaults to `disabled`** — an account
is passkey-only unless someone deliberately opens the other door),
`passwordHash` (argon2id PHC string, written only by `authd`).

**Status:** `credentials[]` — enrolled WebAuthn authenticators, each with the
base64url credential `id`, the COSE `publicKey`, the `signCount` as of the last
assertion, `addedAt`, and an optional human `label`. Public data only: the
private key never leaves the device. Credentials live in status precisely so an
admin editing an account by hand cannot corrupt a key.

**Webhook:** validation refuses to remove the last admin, whether by deletion or
by demotion, and **fails closed** — if the admin list cannot be read, the request
is denied rather than assumed safe.

**Deployment status:** `authd` runs in the `cluster-control` namespace but is
consumed by nothing, and on the current test cluster the
`frameusers.frame.plume-labs.io` CRD is **not installed** — the operator there
predates the type. Until it is applied, every FrameUser read or write returns
NotFound. See the roadmap for the authd stages that switch this on.

---

## FrameService

*Group `services.plume-labs.io`, not `frame.plume-labs.io`.* A declared
instance of a service — inference today; database, queue and VM are future
provider types on the same envelope. Designed in
[`docs/superpowers/specs/2026-08-08-frame-service-catalog-design.md`](superpowers/specs/2026-08-08-frame-service-catalog-design.md).

**One generic CRD, not one per type.** `spec.type` selects a Go provider
(`internal/services/provider/`) registered at manager startup. The provider,
not the CRD's OpenAPI, owns and validates `spec.parameters`.

> **`spec.parameters` is provider-owned and sits outside the API compatibility
> guarantee.** The envelope below it — `type`, `serviceClass`, `binding`,
> `deletionPolicy`, `status` — is what this project's compatibility promise
> covers. A provider that needs a breaking parameter change ships a new
> `type` value rather than redefining an existing one's parameters.

**Spec:** `type` (required, closed set enforced by the webhook against the
provider registry), `parameters` (map[string]string, provider-owned),
`serviceClass` (`HIGH`/`MEDIUM`/`LOW`, default `MEDIUM` — decides scheduling
tier, never a node), `binding.secretName` (defaults to the FrameService's own
name), `binding.projectTo` (namespaces to copy the credentials Secret into,
default none), `deletionPolicy` (`Retain` default | `Delete`).

**Status:** `phase` (Pending/Provisioning/Ready/Degraded/Deleting),
`conditions[]`, `binding.secretRef` + `binding.endpoint` (never a credential)
+ `binding.projected[]` (every Secret coordinate the controller has actually
written — the sole record it consults for what it may write to or must
delete, never a label on the Secret itself), `sizing` (`gpu`, `gpuMemory`,
`cpu`, `memory` — what the provider derived, reported because nothing in the
spec states it), `provisioned[]` (objects the provider created, so
`kubectl describe` explains an instance without knowing the provider's
internals), `observedGeneration`.

**Printer columns:** `TYPE`, `PHASE`, `ENDPOINT`, `AGE`.

**Controller:** dispatches to the registered provider for `spec.type`;
generic reconcile drives create → status → delete through the `Provisioner`
interface. Owns everything that *exposes* the instance (Service, credentials
Secret, projected copies) via owner references, so those are garbage
collected with the FrameService regardless of `deletionPolicy`. Data objects
(a PVC, a delegating operator's CR) never carry that owner reference — that
split is what lets `Retain` keep them. A finalizer holds the object open
until the provider reports what it released.

**Webhook:** validation only, no defaulting. Refuses an unknown `spec.type`,
validates `spec.parameters` against that provider's schema, and runs the
provider's `Size` so an instance that cannot fit is refused by `kubectl
apply` with the numbers named, rather than admitted and left Pending. Also
refuses changing `spec.type` on an existing object — the old provider is no
longer consulted and the new one does not recognise what was provisioned.

### The `inference` provider (`internal/services/provider/inference/`)

The only backend is llama.cpp — the cluster's one GPU is a Tesla P4 (Pascal,
compute capability 6.1), which rules out vLLM and KubeAI (need `sm_7.0`+).
The backend is a provider-internal choice: a future card just means a new
provider behind the same `type`, not an API change.

**Parameter schema** (enforced at admission):

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `model` | string | yes | Enum, closed to the provider's model catalog (currently `llama-3.1-8b-instruct`, `llama-3.1-70b-instruct`, `qwen2.5-7b-instruct`) |
| `contextLength` | string | no | Pattern `^[0-9]+$`; defaults to 4096. Sized against the GPU — too large is refused here, not left to crash-loop |
| `modelCache` | string | no | Name of a PersistentVolumeClaim, already present in the namespace, holding cached GGUF weights. Defaults to `model-cache-pvc` |

Only a deliberately narrow slice of JSON Schema is enforced, on purpose: on
the root schema, `Type` and `Required`; on each property, `Type`,
`Description`, `Enum` and `Pattern`. A provider that sets anything else
(`MinLength`, `Minimum`, `Format`, nested `Properties`, …) panics at registry
construction rather than being silently accepted and never checked.

**Sizing is derived, never chosen** — there is no `plan` field. `Size` reads
`model` and `contextLength` and computes weights + KV cache for that model.
Llama 3.1 8B Instruct at Q4_K_M is 4696Mi of weights; at `contextLength:
"8192"` the KV cache adds 1024Mi, for 5720Mi total, which fits the P4's
7680Mi. The same model at `contextLength: "32768"` would need 8792Mi and is
refused at admission.

**A PersistentVolumeClaim for model weights is required.** The provider
mounts the PVC named by `parameters.modelCache` (or `model-cache-pvc` by
default) read-only at `/models` — it does not create or populate it. If that
PVC does not exist, the instance degrades with reason `ModelCacheMissing`
rather than crash-looping; create the PVC (or point `modelCache` at an
existing one) before the FrameService can become Ready.

**Binding.** The provider generates a 32-byte API token and passes it to
llama.cpp as the `LLAMA_API_KEY` environment variable, keeping it out of the
pod spec (`kubectl describe pod` / Events are a much wider audience than
Secret readers). The token is persisted in a Secret the provider owns,
`<service>-inference-key`, separate from the controller's own binding
Secret, and reused across reconciles rather than regenerated. The binding
Secret the controller writes (`spec.binding.secretName` or the FrameService's
name) carries that same token plus the endpoint — an endpoint alone would
protect nothing while looking like a credential. Rotation is out of scope for
this part: the token is written once at provisioning and stays valid until
the instance is recreated.

**What it creates:** a Deployment running llama.cpp with the sized resource
requests (including `nvidia.com/gpu`) and a node selector derived from
`serviceClass`, a Service in front of it, and the two Secrets above.

---

## Webhooks

FrameNode and FrameJob have **defaulting + validation**; the other six —
SchedulingPolicy, FrameResourceQuota, TalosMachineConfig, TalosUpgrade,
FrameUser, and FrameService — have **validation** only. Validators enforce
required fields and value ranges (or, for FrameService, dispatch to the
provider's own parameter schema) before a CR is admitted. Tests:
`internal/webhook/frame/v1alpha1/*_test.go` and
`internal/webhook/services/v1alpha1/*_test.go`.
