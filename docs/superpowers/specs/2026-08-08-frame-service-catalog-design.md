# FrameService — the service catalog (S1, part 1)

Status: design agreed, not yet implemented
Group: `services.frame.plume-labs.io/v1alpha1`
Gates: Phase B (the `frame.plume-labs.io` API freeze)

## What this is

Frame manages nodes, jobs, quotas and scheduling. It does not manage the things
a workload actually consumes: a database, a queue, an inference endpoint, a VM.
Today each of those is a hand-applied manifest, and there is no way to ask Frame
for one, no record that it exists, and nothing that hands its credentials to the
workload that needs them.

S1 adds that: a resource you create to ask for a service instance, a controller
that provisions it, and a Secret the consumer can mount.

Four requests — inference, database, queue, VM — share one shape: *declare a
desired instance, a controller provisions it, a consumer binds to it*. They are
four types in one catalog, not four projects.

**Part 1 — this spec — is the model plus the inference type.** It is what gates
the API freeze, because a catalog is the most likely thing to prove the core API
wrong. Database, queue and VM are further types on a settled model and gate
nothing; they are out of scope here.

## Why one CRD and not four

Two shapes were considered.

**Typed CRDs** — `InferenceService`, `Database`, and so on — give CEL validation,
printer columns and admission-time errors for free, which is what a freeze can
lock down.

**One generic `FrameService`** with a `type` and a parameter map gives a uniform
catalog, one controller, and lets a new type land without touching the API.

**Decision: the generic `FrameService`.** Its two real costs are addressed rather
than accepted:

- *Unvalidated parameters.* A parameter map that the apiserver cannot check would
  push every mistake from admission to reconcile time, where it surfaces as a
  degraded status ten seconds later instead of a rejected `kubectl apply`. So the
  validating webhook dispatches on `spec.type` to a **per-type parameter schema**
  registered in Go next to that type's provider. Bad parameters are refused at
  admission, with the field named. What the generic CRD gives up is the apiserver
  enforcing it; it does not give up the enforcement.
- *Freezability.* `parameters` cannot be frozen the way a typed field can. Rather
  than pretend otherwise, the compatibility boundary is stated: the **envelope**
  (`type`, `plan`, `binding`, `deletionPolicy`, `status`) is covered by the API
  guarantee; **`parameters` is provider-owned** and versioned with its provider.
  A provider that needs a breaking parameter change ships a new `type` value
  rather than redefining the old one.

## API

```yaml
apiVersion: services.frame.plume-labs.io/v1alpha1
kind: FrameService
metadata:
  name: llama-70b
  namespace: research
spec:
  # Selects the provider. The set of valid values is closed and enumerated by
  # the webhook, so a typo is refused rather than left Pending forever.
  type: inference

  # A named size, resolved by the provider into concrete requests/limits.
  # Provider-defined; the webhook checks it against that provider's list.
  plan: small

  # Provider-owned. Validated at admission against the schema the provider
  # registers, not by the CRD's own OpenAPI.
  parameters:
    model: llama-3.1-70b-instruct
    contextLength: "32768"

  # Which service class this instance's workloads run under, so the existing
  # FrameResourceQuota and SchedulingPolicy apply to it like any other workload.
  serviceClass: HIGH

  binding:
    # Namespaces to project the credentials Secret into, beyond the service's
    # own. Empty means the Secret stays where the FrameService lives.
    projectTo: []
    # Name of the Secret. Defaults to the FrameService name.
    secretName: ""

  # Retain (default) | Delete. See "Deleting an instance".
  deletionPolicy: Retain

status:
  phase: Provisioning        # Pending | Provisioning | Ready | Degraded | Deleting
  conditions: []             # Ready, as every other Frame controller reports it
  binding:
    secretRef: { name: llama-70b }
    # What the consumer actually needs, so a human can see it without opening
    # the Secret. Never contains credentials.
    endpoint: http://llama-70b.research.svc:8080
  # What the provider created, so `kubectl describe` explains the instance
  # without anyone having to know the provider's internals.
  provisioned:
    - { apiVersion: apps/v1, kind: Deployment, name: llama-70b }
    - { apiVersion: v1, kind: Service, name: llama-70b }
  observedGeneration: 3
```

Printer columns: `TYPE`, `PLAN`, `PHASE`, `ENDPOINT`, `AGE`. This is most of what
typed CRDs would have given at the terminal, and it costs five markers.

## Providers

A provider is a Go interface, not a CRD. One implementation per `type`.

```go
type Provider interface {
    // Type is the spec.type value this provider answers to.
    Type() string
    // ParameterSchema is what the webhook validates spec.parameters against.
    ParameterSchema() *apiextensionsv1.JSONSchemaProps
    // Plans enumerates the named sizes this provider accepts.
    Plans() []string
    // Reconcile drives the instance towards the spec and reports what exists.
    Reconcile(ctx context.Context, svc *FrameService) (Result, error)
    // Bind returns the Secret contents and the endpoint for status.
    Bind(ctx context.Context, svc *FrameService) (Binding, error)
}
```

Providers split into two kinds, and the split is not incidental — it is what the
cluster already has:

- **Delegating.** An operator already owns this workload; the provider writes
  that operator's CR and reads its status back. `postgres-operator`
  (`postgresqls.acid.zalan.do`) is installed, so the database type will delegate
  to it rather than reimplement a Postgres operator.
- **Owning.** No operator exists; the provider creates the Deployment, Service
  and PVCs itself and owns their lifecycle through owner references. Inference is
  this kind.

A provider must degrade rather than fail when its backing operator is absent —
the same rule `SchedulingPolicy` already follows for Volcano and YuniKorn. A
missing operator sets `Degraded` with a reason naming the CRD, and does not wedge
the controller.

## The inference provider

The first type, and the one that proves the model.

Today `InferenceView` reads llama.cpp metrics out of Prometheus. That is
monitoring: it can show a server someone else deployed and nothing more. This
provider makes the server itself declarable.

**Hardware constraint, load-bearing.** The cluster's only GPU is a Tesla P4 —
Pascal, compute capability 6.1, 7680 MiB, on one node. That rules out vLLM and
KubeAI, which need `sm_7.0`+. `deploy/caching/vllm-rdma-kvcache.yaml` exists in
the repo and cannot run here. **llama.cpp is the only viable backend**, and the
provider's model must not assume a second one exists. It must also not assume it
never will: the backend is a provider-internal choice, so a future `sm_7.0` card
means a new provider implementation behind the same `type`, not an API change.

What it creates:

- A Deployment running llama.cpp with the model and context length from
  `parameters`, GPU request from `plan`, and the node selector implied by
  `serviceClass`
- A Service in front of it
- A Secret holding the endpoint and, when the plan enables it, an API token

Two operational facts the provider has to respect, both learned the hard way on
this cluster and recorded so the implementation does not rediscover them:

- **Context length is not free.** An oversized `-c` exhausts the KV cache and
  turns into runtime errors rather than a clean rejection. The provider caps
  `contextLength` per plan against the GPU memory the plan reserves, and refuses
  at admission rather than crash-looping.
- **The GPU is shared.** Neura's own inference already occupies this card. A
  second instance on the same node competes for the same 7680 MiB, so the
  provider must request GPU memory explicitly and let the scheduler refuse rather
  than oversubscribe.

## Binding

Credentials land in a Secret in the FrameService's own namespace, named
`spec.binding.secretName` or the service name. A consumer mounts it.

`spec.binding.projectTo` copies that Secret into other namespaces. It is opt-in
and explicit: a service catalog that writes Secrets into namespaces nobody
listed is a cross-tenant leak wearing a convenience feature's clothes. The
controller only ever creates or updates Secrets it owns, tracked by owner
reference and a `frame.plume-labs.io/owned-by` label, so it can never overwrite a
Secret someone else created with the same name.

Rotation is out of scope for part 1. The Secret is written once at provisioning.
When a provider needs rotation, it belongs in that provider, not in the envelope.

## Deleting an instance

`deletionPolicy: Retain` is the default. Deleting a FrameService removes what
Frame created to *expose* the instance — Service, Secret, projected copies — and
leaves the data behind: the PVC, and for a delegating provider the backing CR.
`Delete` removes those too.

Retain is the default because the failure modes are not symmetric. A retained
volume costs disk and is visible in `kubectl get pvc`. A deleted one costs the
data, silently, at the moment someone typed `kubectl delete` meaning to redeploy.
The Zalando operator makes the same choice with its own delete protection.

A finalizer holds the FrameService open until the provider has reported what it
released, so status always explains what was kept.

## Where this may force the core API to change

This is why S1 gates the freeze. Each of these is a hypothesis to be settled by
implementing the inference type — not a change to make up front.

- **FrameResourceQuota.** A service instance consumes GPU, CPU and memory
  indefinitely, unlike a job that ends. `maxGPUs` currently caps a service class
  in aggregate, which may or may not be the right ceiling once long-lived
  instances sit inside it. If a separate ceiling is needed, the field lands
  before the freeze.
- **SchedulingPolicy.** Instances are long-lived and should not be preempted the
  way a batch job can be. Whether that is a new priority class per service class,
  or a field, is open.
- **FrameApplication (S4).** An application will declare the services it
  requires. Whatever `FrameService` exposes for that — a claim, a selector, a
  reference — is the surface S4 binds to, and it is easier to get right now than
  to convert later.

## Testing

- Envtest per provider: spec to created objects to status, plus the degrade path
  when the backing operator's CRD is absent.
- Webhook tests per type: a valid parameter set, and one rejection per schema
  rule, since admission-time validation is the thing the generic CRD had to earn.
- One Kind e2e spec, matching the pattern the `frame.plume-labs.io` CRDs now
  follow: create a FrameService, assert the Deployment and Secret exist and that
  status reports the endpoint. The inference image is not pulled in CI — a stub
  provider registered under a test-only type proves the envelope, and the
  llama.cpp provider is exercised on the real cluster.
- Binding: a projected Secret appears in the listed namespace and nowhere else,
  and a pre-existing Secret of the same name is never overwritten.

## Out of scope for part 1

Database, queue and VM types. Credential rotation. Instance resize after
creation. A UI beyond a read-only list — the catalog's surface belongs with S4,
which is what consumes it.

## Open questions

- Does `plan` stay provider-defined, or become a cluster-wide `ServicePlan`
  resource an admin curates? Provider-defined is enough for one type; a second
  type with overlapping plan names will answer this.
- Should a FrameService be able to target a node explicitly, or only through
  `serviceClass`? Only through `serviceClass` until something needs otherwise.
