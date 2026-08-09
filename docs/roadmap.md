# Roadmap

Frame is a **`v1alpha1` preview**: the operator reconciles eight CRDs across two API groups with webhooks and envtest coverage, the IaC under `deploy/` provisions the cluster, and the UI talks directly to the Kubernetes CRD API.

Two things advance at once:

- **The V1 path** — clear the debt, go real-time, freeze the current API group, package it, release it. This is a narrowing exercise: no new capability enters V1.
- **New API groups** — a service catalog, SDN management, auto-update, and application management. Each lives in its own API group at `v1alpha1` so it can move without blocking the V1 freeze.

They are not fully independent. **The service catalog (S1) comes before the freeze**, because a catalog is the most likely thing to prove the core API wrong — service instances have to be counted by FrameResourceQuota, their workloads have to reach SchedulingPolicy, and FrameApplication has to bind to them. Freezing before learning that buys a conversion to v2 later.

The gate is narrow on purpose: the freeze waits for S1's *model* to settle, proven by one service type, not for all four types to ship. Otherwise V1 waits on KubeVirt.

Running both tracks with a single operator means alternating between them, not advancing both at once. Whichever gets attention, the other waits.

The bar for each phase is its **Exit criteria** — a phase is not done until those are demonstrable, not just coded.

---

## Current state (baseline)

- ✅ 7 CRDs in `frame.plume-labs.io/v1alpha1`: FrameJob, FrameNode, FrameResourceQuota, FrameUser, SchedulingPolicy, TalosMachineConfig, TalosUpgrade — with generated manifests and RBAC tiers
- ✅ Every CRD has a controller producing real cluster effects, with finalizers, Kubernetes Events and Prometheus metrics
- ✅ Validating/defaulting webhooks + envtest coverage threshold ≥ 45% on `internal/controller`, tracked in CI
- ✅ UI (13 tabbed screens behind an Overview landing) and SDK talk directly to the Kubernetes API — no intermediate server. Dev: `kubectl proxy`. Prod: ServiceAccount Bearer token.
- ✅ `deploy/` IaC: Talos, Ceph (RGW), Cilium/RDMA, Argo, monitoring, GitOps
- 🚧 **authd is built and deployed but inert.** Argon2id passwords, ES256 OIDC issuer with JWKS, usernameless WebAuthn, FrameUser store with a last-admin webhook guard — all merged and running in `cluster-control`, consumed by nothing. Stage 2 (configuring the apiserver to trust it) and Stage 3 (switching the UI over) are unwritten.
- 🚧 Bare-metal provisioning (`deploy/omni/`): manifests and install script ready, never executed — Omni manages Talos machines, the test cluster is k3s, and there is no bare metal yet. Sidero Metal was dropped: upstream no longer develops it. `deploy/pxe/` is still only a README.

---

## The V1 path

V1 means: a frozen, conversion-guaranteed API for `frame.plume-labs.io`; live status instead of polling; and a repeatable, secured release. It does **not** mean per-user authentication — see the RBAC note in Phase B.

### Phase A — Clear the debt ✅ DONE

Nothing here is new capability. It removes claims the code no longer backs, and proves the claims it does.

- [x] Delete `deploy/api/openapi.yaml`. It documented a REST API on `localhost:4000` served by an Express server that was removed. Keeping a spec for an API that does not exist is worse than having no spec. `PRD.md` described the same absent server and was corrected with it — the CRDs are the API, and their OpenAPI schema is served by the apiserver from the kubebuilder markers.
- [x] FrameResourceQuota's scheduler queue limits: **cut**. The controller already projects `corev1.ResourceQuota` into every matching namespace; `SchedulingPolicy` already reconciles Volcano/YuniKorn queues. Duplicating the ceiling into a queue makes two resources authoritative for one number, and no caller asked for it. Settling the CRD also turned up a real defect: `maxJobs` was projected as a pod quota, so a single FrameJob fanning out through its ArgoWorkflow could exhaust the whole service class. It now maps to the object-count quota `count/framejobs.frame.plume-labs.io`.
- [x] Extend the Kind e2e suite to cover each CRD. It covered manager startup, the metrics endpoint, cert-manager provisioning and webhook CA injection; it now runs one spec per CRD against a real apiserver, 12 of 12 green. Writing them found a defect no unit test could: the FrameNode validating webhook demanded a network before discovery, which made node provisioning unreachable through the API. The six sample CRs were `# TODO(user)` stubs that fail validation, so the documented `kubectl apply -k config/samples/` smoke test could not have worked either; they are real now, and FrameUser has one.
- [x] `make lint` is green. The reported 21 was itself understated — golangci-lint caps at three per category, so each fix uncovered more of the same. Two deprecations are deliberately not migrated and carry a `nolint` with its reason: `GetEventRecorderFor`'s replacement returns the newer events API with a different signature, touching all seven reconcilers and every event-asserting test, and Talos's `LifecycleClient` is a raw gRPC stub whose adoption means reimplementing the error-shape handling the upgrade path's idempotency guard reads. Both are behaviour changes wearing a linter's clothes.
- [x] Document FrameUser and correct the CRD count. Writing the reference entry first, then fixing the count, turned up that not every "six" was wrong in the same direction: six of the seven CRDs have controllers (FrameUser has none), all seven have validating webhooks but only two have defaulting, and the docs stated each of those as "all six".
- [x] Close the SDK's coverage gap. `frame-sdk.ts` read and wrote four kinds; TalosMachineConfig and TalosUpgrade appeared nowhere in `src/`, so the two CRDs driving node OS configuration and upgrades had no control-plane surface and `kubectl` was the only way to see whether a patch had landed. `frame.talos` now lists both and a read-only panel on the Nodes screen shows each operation's outcome. Issuing one is left to the provisioning wizard, which already holds a node's endpoint and secret. FrameUser stays out: it is authd's record, not a control-plane resource.
- [x] Install the FrameUser CRD on the test cluster. Done by redeploying the operator rather than applying the CRD alone: the deployed `ValidatingWebhookConfiguration` also predated the type, so a bare CRD apply would have given authd a store with **no last-admin guard in front of it**. `frameusers` and `frameservices` are now installed and the operator runs `frame-controller:3eedbbd`. authd still has no consumer — that is Stages 2 and 3 — but it is no longer writing into a void.

**Exit:** no document describes an API that does not exist, and every CRD is proven spec → cluster effect → status on Kind in CI.

### Phase B — Freeze the API, lock RBAC

**Gated on S1.** Do not start this until the service catalog's model is settled and proven by the inference type, and any core-API changes it forces have landed. See S1 under "New API groups" below.

- [ ] Freeze field names and semantics; remove speculative fields
- [ ] Fold in whatever S1 proved the core needs — quota accounting for service instances, scheduling for service workloads, the binding surface applications consume
- [ ] Introduce `v1beta1` with a conversion webhook and a documented storage version
- [ ] Add CRD field validation (CEL / kubebuilder markers), printer columns, defaults
- [ ] Write a deprecation and migration policy; promote to `v1` once `v1beta1` is stable
- [ ] Lock the viewer/editor/admin RBAC tiers to the final schema, and extend them to the new API groups as those land

**On what RBAC means in V1.** The tiers exist as manifests, but the UI authenticates with a single ServiceAccount token — so the tiers are not currently enforced against any human. V1 delivers *correct, frozen, tested tiers*. Enforcing them per user requires authd Stages 2 and 3, which are post-V1. V1 documentation must say this plainly rather than advertise RBAC it does not enforce.

**Pre-freeze cleanup (done ahead of the phase proper).** A survey of `frame.plume-labs.io/v1alpha1` turned up several fields worth cutting or hardening before conversion makes that expensive. Landed: `FrameNode.Spec.ServerClassRef`/`Status.ProviderID`/`Status.TalosVersion`/`Status.LastHeartbeat`, `FrameJob.Spec.Name`, `SchedulingPolicy.Spec.GangScheduling`, and `TalosUpgrade.Spec.PreserveData` removed (each had no reader anywhere in the controller, SDK, or UI — a follow-up review found the first pass had missed two *writers*, `test/e2e/e2e_test.go` and `deploy/samples/test-cluster/workloads.yaml`, both now fixed); CEL/kubebuilder validation pushed down for most rules a webhook already enforced in Go (conditional-required FrameNode network fields including a real `isIP()` check on `network.dns[*]`, `SchedulingPolicy` preemption/priorityClass, `FrameResourceQuota`'s at-least-one-limit rule, `TalosMachineConfig`'s configPatch/configPatchRef oneof, `host:port` — IPv6 included — and image-tag patterns); printer columns added to the four kinds that had none (`SchedulingPolicy`, `FrameResourceQuota`, `TalosMachineConfig`, `TalosUpgrade`). **The GPU/`serviceClass:LOW` conflict was deliberately *not* pushed into CEL**, unlike the others: the webhook only enforces it for pipelines in a known-list and returns early with a warning for everything else (including `training`, this project's own sample and e2e pipeline), so a CEL mirror rejected objects the webhook has always accepted and would have permanently stranded stored ones. This constraint's real state — it silently doesn't apply to most pipelines today — is now Phase B's to resolve, not this pass's. **`GangScheduling` specifically:** it was validated (required `queueName` alongside it) and shown in the UI, but no controller ever created a Volcano/YuniKorn `PodGroup` or set a `minMember` — the field gated an input-shape rule with zero cluster-side effect. Gang scheduling is unimplemented. It belongs on `FrameJob`, not `SchedulingPolicy`, when someone builds it — gang scheduling is a property of the job being scheduled, not of the queue/priority policy it schedules through. Left alone: renames (e.g. `FrameJob.Spec.ServiceClass` vs `FrameNode.Spec.ServiceClass` enum asymmetry, `FrameJob`'s `"Submitted"` condition-type outlier), the `status.phase`-vs-conditions-only split across kinds, the missing top-level `observedGeneration`, the cross-namespace reach of `TalosSecretRef.Namespace`/`FrameJob.Spec.Namespace` (form-validated now, but whether the controller should be allowed to act across namespaces at all is this phase's RBAC-tier question, not a pre-freeze one), whether `TalosSecretReference.Name` should become `Required` (an early draft did this by accident; reverted), and the FrameJob GPU/LOW bypass above — all deferred to this phase's own design work. Full detail, including a Fix Round 1 section covering a review pass's findings, in `.superpowers/pre-freeze-cleanup-report.md`.

**Exit:** `v1` is the storage version, conversion is tested round-trip, and a documented upgrade path exists from any earlier alpha.

### Phase C — Real-time

Runs in parallel with Phase B: it changes the SDK surface, not the CRD schemas.

The previous roadmap called for an "SSE/Watch endpoint". There is no server left to host one. The apiserver the UI already talks to serves watch streams natively (`?watch=true`, chunked), so this is a client change with nothing new to deploy.

It also called the current behaviour polling. It was not: `useLiveResource` fetched once and offered a manual `reload`, so outside the provisioning wizard's own loop **nothing in the UI ever refreshed itself**. A job that finished stayed running on screen until someone reloaded the page.

- [x] Watch streams in `src/lib/k8s-watch.ts`, read straight off the apiserver
- [x] Handle `resourceVersion` expiry with a re-list, reconnect with backoff, and fall back to an interval when a watch cannot be established at all
- [x] Convert the screens that change fastest first: FrameJobs (phase transitions), FrameNodes (plus the core `Node` its status mirrors), Events
- [x] Convert the remaining screens that read live cluster state. Twenty-two views, each given the mechanism its data actually supports: a watch for Kubernetes objects, an interval for metrics and proxied third parties, chosen per screen from how fast the thing moves. The split needed one correction — a fetcher reading both an object and a metric needs both, or its gauges freeze until an object changes, which is the failure the split existed to prevent.

A watch here is a change signal, not a data source: an event re-runs the fetch the view would have run anyway, rather than applying deltas to a local cache. Every view keeps its existing mapping code, at the cost of one list per change — free at this scale, and the note in `k8s-watch.ts` says what to do if a screen ever watches something that churns per second.

**Exit:** a FrameJob phase transition appears in the UI with no reload, and killing the stream mid-flight recovers on its own.

### Phase D — Production hardening and release

- [ ] Packaging: versioned image + Helm chart with upgrade tests. **No chart exists today** — deployment is kustomize overlays only.
- [ ] Cert-manager-backed webhook TLS verified in-cluster
- [ ] HA manager (leader election already wired) validated under failover
- [ ] Backup/restore (Velero) and checkpoint/IPMI-watchdog paths exercised in e2e
- [ ] Security review: RBAC least-privilege, NetworkPolicies, image scanning, SBOM
- [ ] Docs: install guide, API reference, runbook, upgrade guide

**Exit:** a tagged `v1.0.0` installs from a published chart on a fresh cluster, passes e2e, and survives a manager failover and an upgrade from the prior release.

---

## New API groups

Four capabilities that Frame does not have. Each gets its own API group at `v1alpha1`, versioned independently of `frame.plume-labs.io`, so building them never blocks the V1 freeze. Each gets its own spec before any code.

Four of these requests share one shape — *declare a desired resource, a controller provisions it, a consumer binds to it*. Databases, queues, inference servers and VMs are not four projects; they are four types in one catalog.

### S1 — Service catalog (`services.plume-labs.io/v1alpha1`)

Declarative provisioning of service instances with a lifecycle and credential binding: **inference, database, queue, VM**.

**This is the one new group that precedes the V1 freeze**, and it is sliced so that only its first part does:

1. ✅ **Done. The model + inference** — the instance/binding shape, and one type implemented against it. Designed in [`docs/superpowers/specs/2026-08-08-frame-service-catalog-design.md`](superpowers/specs/2026-08-08-frame-service-catalog-design.md): one generic `FrameService` CRD, a Go provider per type, per-type parameter schemas validated at admission, and a stated compatibility boundary around `parameters`. Today `InferenceView` reads llama.cpp metrics from Prometheus — monitoring only, no provisioning. Managing inference means declaring a model server and having Frame stand it up. This part gates Phase B: whatever it proves the core API needs must land before the freeze. See "What implementing S1 proved" below for the answers.
2. **Database, queue, VM** — further types on a settled model. These do not gate anything. VM implies KubeVirt, which appears nowhere in the repo: greenfield, and the reason V1 must not wait for the full catalog.

Note the hardware constraint on the inference type: the current GPU is a Pascal P4 (`sm_6.1`), which rules out vLLM and KubeAI. `deploy/caching/vllm-rdma-kvcache.yaml` exists but cannot run here. llama.cpp is the only viable backend until the hardware changes — the model must not assume otherwise.

#### What implementing S1 proved about the core API

The design spec posed three hypotheses under "Where this may force the core API to change," to be settled by implementing the inference type rather than guessed up front. Implementing it gave a load-bearing answer for two, and left the third open on purpose:

- **FrameResourceQuota — turned out not to matter.** No change was made to `FrameResourceQuota`'s API or controller in this branch. `maxGPUs` maps straight to a Kubernetes `ResourceQuota`'s `requests.nvidia.com/gpu` hard limit (`internal/controller/frame/frameresourcequota_controller.go`), and that primitive already meters *currently outstanding* requests for as long as a pod holds them — it does not distinguish a job that ends from an instance that does not. A `FrameService`'s inference pod requests `nvidia.com/gpu` on its container (`internal/services/provider/inference/inference.go`), which Kubernetes mirrors into the pod's `requests` the same way for any workload, so it consumes exactly one accounting slot in its namespace's quota for exactly as long as it runs. The aggregate ceiling the spec worried about was already the right ceiling; nothing new to fold in.
- **SchedulingPolicy — the mechanism gap is proven; whether it is exploitable today is not, and Phase B has to close the gap either way.** What the code actually shows: `SchedulingPolicy`'s `reconcilePriorityClass` (`internal/controller/frame/schedulingpolicy_controller.go`) creates a `PriorityClass` named by the CR with `value` defaulting to `0` unless `spec.priorityValue` is set, and always sets `GlobalDefault: false` — so no `frame-*` class is ever the cluster's implicit default. `FrameJob` has a lever to reach one of these classes: `jobPriorityClass` (`internal/controller/frame/framejob_controller.go`) maps `spec.priority` to `frame-{priority}` and sets it as the Workflow's `priorityClassName`. **The `inference` provider has no equivalent anywhere in `internal/services/provider/inference/` — it never sets a `priorityClassName` at all**, so its pod always runs at the implicit Kubernetes default (`0`, since nothing here installs a `GlobalDefault` class), with no field or parameter able to change that. Whether that is *currently* exploitable depends on operator-configured `SchedulingPolicy` values this repository does not control or guarantee — the `deploy/kubernetes/scheduling/priority-classes.yaml` manifest sometimes cited for this is static IaC for unrelated `neura-*` namespaces, applied by `deploy/scripts/neura-bootstrap.sh`, and says nothing about what value any `frame-*` class actually holds on a given cluster. What the code does prove, independent of any cluster's configuration: a `FrameService` instance has no way to ask for scheduling protection that `FrameJob` already has. **Phase B must give `FrameService` a `serviceClass` → `PriorityClass` mechanism symmetric to `FrameJob`'s `priority` → `PriorityClass` one** — not because current values are known to be unsafe, but because today there is no lever an operator could turn to make a long-lived instance non-preemptible even if they wanted to.
- **FrameApplication (S4) — settled as "reference," pending S4 itself, and proven only up to where the code proves it.** `status.binding` (`api/services/v1alpha1/frameservice_types.go`) already publishes exactly a reference shape: `secretRef` (a same-namespace `LocalObjectReference`, name only) plus a credential-free `endpoint`, and `spec.binding.projectTo` lets the owning `FrameService` push a copy of its credentials `Secret` into a consumer's namespace on request. The envtest suite in `internal/controller/services/binding_test.go` exercises this directly — writing the Secret, projecting it into listed namespaces only, never overwriting a Secret it does not own, and pruning a projection when a namespace leaves `projectTo` — so the mechanism itself is proven, independent of any specific provider reaching `Ready`. (The Kind e2e inference spec is not that evidence: its own comment records that the pod never reaches `Ready` in Kind, so `Bind` is never called and `status.binding.secretRef` is deliberately never asserted there.) That is the surface S4 binds to: a `FrameApplication` names a `FrameService` and reads its `status.binding`, requesting a projection into its own namespace rather than needing a selector or a claim object. No envelope field had to change to make this workable, and nothing in the inference type argued for a claim or label-selector model instead — S4 can consume what already exists.

### S2 — SDN (`net.plume-labs.io/v1alpha1`)

`deploy/networking/` is static YAML — Cilium, Multus, SR-IOV, RDMA device plugin, PTP, DPDK — applied once, with no management surface. S2 turns segmentation, policies and network attachments into declared resources a controller reconciles.

### S3 — Auto-update

Frame itself. `TalosUpgrade` already covers the node OS; nothing covered the operator, the UI or authd. Designed as two pieces, neither of which is an API group:

- a [release chain](superpowers/specs/2026-08-09-frame-release-chain-design.md) publishing the three images to GHCR from a git tag — a prerequisite, and the place where CI's own defect gets fixed: `build.yml` builds the root Dockerfile, which is the UI, and publishes it under the bare repository name as though it were the project, while the operator and authd are built by CI nowhere
- an [update screen](superpowers/specs/2026-08-09-frame-update-screen-design.md) showing what runs, what is available, and what an update would disturb right now

No self-updating operator: the UI patches the Deployment, so the bootstrap problem — a controller surviving its own rollout mid-reconcile — never arises. No new CRD either, which means **S3 does not gate the Phase B freeze**, contrary to the assumption behind the phase ordering.

Argo CD already converges declared versions and this does not compete with it. What Argo cannot see is whether now is a moment the cluster can afford the interruption. Frame can, by reading its own resources. That panel is the point; the button is the easy part.

The addons under `deploy/` — Argo, cert-manager, Volcano, Cilium, the Postgres operator — stay out. They are Argo CD's to converge, and Frame has nothing to add about them that Argo does not already know.

### S4 — Application management

Three layers of one subsystem:

1. **`FrameApplication` CRD** — the model. An application declares its components, the services it requires (binding to S1), its quotas and its scheduling policy; a controller reconciles it.
2. **Deploy from Frame** — the action. Install and update an application (Helm chart or manifests) from the UI, with Frame owning the lifecycle.
3. **A grouped view** — the surface. Today `ApplicationsView` lists Deployments read through the SDK. Group by application rather than by Deployment, with health, logs and rollback.

Layer 3 ships on its own, immediately: it is UI grouping over reads that already work, and needs neither the CRD nor S1. Layers 1 and 2 follow S1, since an application binding to services requires services to exist.

### Phase E — Stabilize the new groups

The same ritual as Phase B — conversion webhook, CEL validation, printer columns, RBAC tiers — applied to S1–S4 once each surface has settled. Without this, the new groups stay alpha indefinitely and the point of separating them is lost.

---

## V2

**Multi-site / multi-region federation.** Note that `PRD.md` currently declares this out of scope — "Frame manages a single local cluster", with an RDMA fabric that is local by construction and does not traverse the WAN. V2 will have to contradict the PRD; that argument belongs in the V2 spec, not here.

---

## Post-V1, unscheduled

- authd Stages 2 and 3 — per-user authentication and enforced RBAC
- `deploy/omni` execution — blocked on hardware: no bare metal, and the test cluster is k3s
- `deploy/pxe` — README only
- Autoscaling FrameNodes from spare bare metal
- Cost/capacity forecasting beyond the current dashboards

---

## V1 definition of done

1. `frame.plume-labs.io` is `v1` with tested conversion from alpha and beta.
2. UI and SDK drive real CRDs over watch streams; no polling and no simulation in the default path.
3. All seven controllers produce real, observable cluster effects with metrics and events, each proven end-to-end on Kind in CI.
4. One-command install from a published, versioned Helm chart.
5. CI is green across build, lint, unit/envtest, and Kind e2e.
6. Security and upgrade paths reviewed and documented, including an explicit statement that RBAC tiers are not yet enforced per user.
