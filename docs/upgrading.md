# Upgrading

Four different things get called "upgrading" here, and they don't share a
procedure: moving today's kustomize install onto the Helm chart, moving from
one chart version to the next, moving an existing install across the
`v1alpha1` → `v1beta1` API freeze, and the parts still not covered. Each gets
its own section below.

Everything marked "verified" in this doc was run against either the live k3s
test cluster (read-only checks and `--dry-run=server` only — nothing on that
cluster was created, changed, or deleted) or a disposable local kind cluster
built and torn down for this purpose. Nothing here is inferred from the
manifests alone.

---

## 1. Migrating an existing kustomize install to Helm

This is the situation that exists today: the live test cluster's
`frame-controller-manager` was installed by `make deploy`
(`kubectl apply -k config/default`), and carries
`app.kubernetes.io/managed-by: kustomize` — confirmed by reading it directly.
No Helm release exists there yet.

The chart was deliberately built to make this migration possible: every
object it renders uses the same fixed `frame-` name kustomize's
`namePrefix: frame-` produces, instead of the usual Helm pattern of deriving
names from the release name. See "Resource names are a fixed `frame-`
prefix" in `charts/frame/README.md` for the full reasoning — it is not
repeated here. That's what makes this the adoption command:

```bash
helm install frame charts/frame -n frame-system --take-ownership \
  --set image.repository=<the image currently running> \
  --set image.tag=<the tag currently running>
```

`--take-ownership` tells Helm to adopt existing objects that match by name
instead of failing on the conflict. Match the currently-running
`image.repository`/`image.tag` (and any other non-default values already in
effect — `replicaCount`, `metrics.secure`, etc.) on this first install, or
the adoption also changes the running configuration in the same step.

### What has been verified, and what has not

**Verified — structurally, on every commit.** `hack/helm-parity.sh` renders
both `helm template` and `kustomize build config/default`, diffs the
`kind|namespace|name` set, then diffs the full body of every shared
resource. CI fails the build if either side ever grows something the other
lacks, or if a shared resource's body drifts (a dropped RBAC verb, a changed
probe port, a missing `--leader-elect`). This is what makes the adoption
command above trustworthy in principle — see `charts/frame/README.md`,
"Anti-drift check," for exactly what the script does and does not compare.

**Verified — this session, against the live test cluster, without mutating
it.** Running the adoption command as a server-side dry run against the
cluster that is actually running the kustomize install:

```bash
helm install frame charts/frame -n frame-system \
  --set image.repository=<current image> --set image.tag=<current tag> \
  --dry-run=server
```

fails, as expected, with:

```
Error: INSTALLATION FAILED: unable to continue with install: ServiceAccount "frame-controller-manager"
in namespace "frame-system" exists and cannot be imported into the current release: invalid ownership
metadata; label validation error: key "app.kubernetes.io/managed-by" must equal "Helm": current value is
"kustomize"; annotation validation error: missing key "meta.helm.sh/release-name": must be set to "frame";
annotation validation error: missing key "meta.helm.sh/release-namespace": must be set to "frame-system"
```

Adding `--take-ownership` to the same server-side dry run succeeds — Helm
renders the full manifest, the API server admits every resource under
`--dry-run=server`, and NOTES.txt prints normally. Afterwards, `helm list -n
frame-system` was still empty and the Deployment's `managed-by` label was
still `kustomize`: the dry run changed nothing.

**Verified — this session, but not on the live cluster.** A disposable local
kind cluster (created and deleted solely for this check) was used to run the
*real*, non-dry-run sequence once: `helm install` (fresh, no prior kustomize
objects to adopt) → CRDs present → cert-manager issues the webhook cert →
`helm upgrade` with changed values → `helm uninstall`. That confirms the
chart's install/upgrade/uninstall mechanics work end-to-end. It does **not**
confirm the adoption path specifically, because a fresh kind cluster has no
pre-existing kustomize objects to take ownership of.

**Not verified, and not attempted.** Actually running
`helm install --take-ownership` (without `--dry-run`) against the live test
cluster to adopt the real, running kustomize objects. That cluster runs
other workloads and this migration was explicitly out of scope for this
session — the dry-run evidence above is as far as this doc goes. Before
doing it for real: back up `frame-system` (Velero, or at minimum
`kubectl get -o yaml` every object in the namespace plus the eight CRDs),
run the adoption command for real, then confirm
`kubectl -n frame-system get deploy frame-controller-manager -o
jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}'` now reads
`Helm` and `helm list -n frame-system` shows the release — and follow the
"Is it healthy?" checks in [runbook.md](runbook.md), which cover the leader
lease, the certificate, and the webhook CA match this doc does not repeat.

---

## 2. Chart-to-chart upgrades

```bash
helm upgrade frame charts/frame -n frame-system \
  --set image.repository=<registry>/frame-controller \
  --set image.tag=<new-tag>
```

**Verified this session**, on the same disposable kind cluster: upgrading a
running release with `--set replicaCount=2 --set
podDisruptionBudget.enabled=true` scaled the Deployment to 2 replicas and
created the PodDisruptionBudget immediately, and `helm history` showed a
second, `deployed` revision superseding the first.

### CRDs

This chart's CRDs are **not** in Helm's special `crds/` directory — they
live at `charts/frame/files/crds/*.yaml` and are rendered by
`templates/crds.yaml`. That is a deliberate choice, not an oversight: Helm
installs the magic `crds/` directory once and never touches it again on
`helm upgrade`, which would make CRD schema changes (Phase B's whole
purpose — new fields, tightened validation, eventually a new version)
un-shippable through a normal upgrade. Rendering from `templates/` means
`helm upgrade` **does** apply CRD changes, the same as any other templated
resource. The full reasoning is in "CRDs are rendered from `templates/`, not
Helm's `crds/` directory" in `charts/frame/README.md` — it is not repeated
here. Section 3 is the first schema change to go through that mechanism, and
it is the one the choice was made for: the chart now ships two-version CRDs
with `conversion.strategy: Webhook` and a cert-manager-injected `caBundle`,
which Helm's magic `crds/` directory would have installed once and never
updated.

### `helm.sh/resource-policy: keep`

Every CRD is stamped with this annotation. Its effect on `helm upgrade` is
none — it only matters on `helm uninstall`, where it makes Helm skip
deleting the CRD (and, by extension, protects every CR of that kind from a
Kubernetes cascade delete). Verified this session: uninstalling the release
on the kind cluster removed the Deployment, RBAC, and webhook configuration
but left all eight `plume-labs.io` CRDs in place, and `helm uninstall`
printed each one under "kept due to the resource policy." Nothing about
`resource-policy: keep` is specific to upgrades — it is mentioned here
because "what happens to my CRDs" is the question people actually ask when
planning an upgrade, and the answer is the same regardless of how many
`helm upgrade`s came before the eventual `helm uninstall`.

---

## 3. API versions and the migration path

Frame's API is frozen at **`v1beta1`**, in both `frame.plume-labs.io` and
`services.plume-labs.io`, on all eight kinds. `v1beta1` is the storage
version and the conversion hub; `v1alpha1` is still served, is marked
`deprecated: true`, and emits a warning on every read and write naming what
changed for that kind.

**`v1` is deliberately not part of V1.** Frame is in beta and needs
capability before it needs a stability promise it cannot yet keep; promotion
waits until `v1beta1` has survived real use, not until a date arrives.
Shipping V1 on `v1beta1` says that honestly, where a `v1` issued on schedule
would not. Concretely, what would justify promotion: `v1beta1` carried by
installs other than this project's own test cluster, for long enough that a
missing field or a wrong bound would have surfaced; the `parameters` maps
(see below) either bounded per-provider or accepted as permanently
out-of-guarantee; and `FrameUser`'s password hash moved out of the CR into a
`Secret`, because that move is a spec change on a `v1` kind and there is no
reason to spend a conversion on it later when it can be spent now. None of
those has happened.

### What the guarantee is

Within `v1beta1`:

- No field is renamed, removed, or given a new meaning.
- No validation is tightened. It may be loosened.
- New optional fields, new status fields, new printer columns and new enum
  *values* may appear in any `v1beta1.z`. They require no conversion and old
  clients ignore them.
- Condition `type` strings are part of the contract. Every kind that has a
  controller writes `Ready`; its `reason` vocabulary is documented per kind
  in [crd-reference.md](crd-reference.md), and clients branch on it now that
  `status.phase` is gone.

Two carve-outs, both deliberate:

- **`spec.parameters` on `FrameJob` and `FrameService` is outside the
  guarantee.** The envelope is bounded — at most 64 keys, each value at most
  1024 characters — but the *meaning* of a key belongs to the pipeline or the
  provider, not to this API. A provider needing a breaking parameter change
  ships a new `spec.type` value rather than redefining an existing one's
  parameters.
- **The validation tightening happened at the version bump, and that was the
  point of doing it there.** `v1beta1` added bounds `v1alpha1` did not have
  (`rack`/`zone` length and pattern, a CEL `isIP()` on `spec.ip`, ceilings on
  `gpuCount`/`maxGPUs`/`queueWeight`, a length cap on `email`, form patterns
  on `pipeline` and `FrameService.spec.type`). Those are the last tightenings
  this API group gets; the rule above starts from here.

### Upgrading from `v1alpha1`

**Order matters, and getting it wrong is a full outage for the affected
kinds.** A two-version CRD declaring `conversion.strategy: Webhook` with
nothing answering `/convert` fails *every* read and write of that kind, not
just the conversions.

1. Roll out the manager that serves `/convert` — the image from this release.
   `helm upgrade` does both in one apply, which is fine because the CRDs are
   inert until something reads them; a kustomize install must not apply
   `config/crd/bases/` from a checkout whose manager image is not yet
   deployed.
2. Let cert-manager issue the webhook certificate and confirm the `caBundle`
   is injected into all eight CRDs. `make helm-parity` proves the manifests
   agree; the cluster-side check is in [runbook.md](runbook.md).
3. Only then migrate the storage version.

Existing objects keep working from step 1 — the apiserver converts them on
read — but they are still *stored* at `v1alpha1` until something rewrites
them, and a version cannot be removed from a CRD while it appears in
`.status.storedVersions`.

```bash
./hack/migrate-storage-version.sh            # dry run, changes nothing
./hack/migrate-storage-version.sh --apply
```

Run it as a cluster administrator: it needs `patch` on every Frame resource
*and* `patch` on `customresourcedefinitions/status`, and no shipped role
grants the second (deliberately — see
[deployment.md](deployment.md), "Running the storage-version migration").
[runbook.md](runbook.md), "Migrating the storage version", is the reference
for what the script does, including the kinds that have no objects at all and
are therefore never rewritten.

**One precondition the script cannot check for you: the Argo Workflows.**
Rewriting a FrameJob re-triggers its controller, which is wanted — see the
FrameJob note below — but if a completed job's Workflow has been
garbage-collected, the controller's `IsNotFound` branch creates a new one and
silently re-runs the job. List them first:

```bash
kubectl get framejobs -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,WF:.status.argoWorkflowName
kubectl get workflows.argoproj.io -A
```

### What changed between `v1alpha1` and `v1beta1`

Nine changes, each announced by the deprecation warning on the version you
are leaving:

| Change | Effect on a `v1alpha1` client |
|---|---|
| `status.phase` removed from FrameJob, FrameNode and FrameService | Still readable at `v1alpha1`, computed from `status.conditions` on the way down. Never stored. Writing it has no effect. |
| `FrameJob.spec.namespace` removed | Ignored. The Argo Workflow is created in the FrameJob's own namespace. A read at `v1alpha1` returns the object's own namespace, not the value you set. |
| `TalosSecretReference.namespace` removed | Ignored. The Secret is read from the CR's own namespace, which is what an empty value always meant. A read returns empty. |
| `TalosSecretReference.name` now required | A `v1beta1` write without it is rejected. |
| `FrameUser.spec.passwordHash` moved to `status.passwordHash` | Read-only at `v1alpha1`. A write that changes it is rejected by the validating webhook, and so is a full replace that omits it — omitting it would erase the credential. Set it through the `v1beta1` `frameusers/status` subresource. Reading it is unaffected — see below. |
| `FrameNode.spec.serviceClass` no longer accepts `""` | Omit the field instead; absence means unclassified. |
| `FrameJob.spec.serviceClass` and `spec.priority` default in the schema, not only in the webhook | Unchanged values (`LOW`, `medium`), but they are now applied before CEL rather than after. |
| The GPU / `serviceClass: LOW` constraint deleted | A GPU job at `LOW` is admitted. It was only ever enforced for three pipeline names, so it never applied to `training` — this project's own sample. |
| `topology.kubernetes.io/rack` on Nodes → `frame.plume-labs.io/rack` | Any selector you wrote on the old key must be updated. The controller removes the old key on reconcile. `rack` is not a well-known `kubernetes.io` key and that prefix is reserved for upstream. |

### Two client-visible breaks that are not schema changes

Both come from the SDK moving to `v1beta1` and are visible in the UI:

- **`JobSpec.namespace` now steers the request URL.** It used to be copied
  into the removed `spec.namespace` field and otherwise ignored; the SDK now
  uses it as the namespace the FrameJob is created *in*. This is the correct
  behaviour — the Jobs view's Retry button passes the original job's
  namespace, and ignoring it would silently relocate every retried job to the
  default — but it is a behaviour change for any caller that was passing a
  namespace it did not mean.
- **The two FrameJobs already stored on the test cluster will display
  `queued`, not `completed`, until they are re-reconciled.** They predate the
  change that made `Ready` track the lifecycle (F3): they carry a write-once
  `Submitted` condition, no `Ready` condition, and a `status.phase:
  Completed` that is not derivable from anything else in the object. The
  outcome is genuinely not in the object, and a projection that guessed it
  from `completionTime` would report an old *failure* as healthy, since the
  controller sets that field on both outcomes. The storage migration's
  rewrite forces the re-reconcile that restores the real answer.

### The deprecation policy

`v1alpha1` is served for at least **two minor chart releases** after the
release that introduced `v1beta1`, and is removed no earlier than the first
release in which every install this project knows about reports
`storedVersions: ["v1beta1"]`. The `deprecationWarning` on each `v1alpha1`
kind is the policy's only enforcement mechanism; it costs nothing and it is
what a client sees before the removal rather than after.

**Removal is a hand-run migration plus an explicit claim, not a version
bump.** `.status.storedVersions` only grows. The apiserver appends the
storage version the moment an *apply* makes that version storage — not when
an object is written at it — and nothing ever prunes the list. So dropping
`v1alpha1` from `spec.versions` requires running
`hack/migrate-storage-version.sh --apply`, whose final step patches
`status.storedVersions` to `["v1beta1"]`. That patch is a **claim** that
nothing remains stored at the old version. The script does what it can to
make the claim true and refuses to half-finish — it aborts rather than treat
a failed listing as "no objects", it refuses a CRD whose storage version is
not the one it is migrating to, and it exits non-zero if a CRD does not end
at a single stored version — but the apiserver does not verify it. If you
patch and you were wrong, the objects you missed are unreadable.

**The script has not been run against the live cluster.** It was rehearsed on
a throwaway Kind cluster carrying the pre-freeze CRDs plus copies of the live
cluster's nine objects, where all eight CRDs went `["v1alpha1"]` →
`["v1alpha1","v1beta1"]` on apply → `["v1beta1"]` after migration. The live
k3s test cluster still runs the pre-freeze operator and single-version
`v1alpha1` CRDs; the whole of section 3 is untried there.

### The one thing the deprecation window costs

**While `v1alpha1` is served, it is the only way to violate a `v1beta1`
bound.** CR schema validation runs against the *request* version, and
conversion-webhook output is stored **without re-validation**. So every bound
`v1beta1` adds that `v1alpha1` lacks — the `rack`/`zone` patterns, the
`isIP()` rule, the numeric ceilings, the `parameters` envelope — is
enforceable only against clients that already moved. A `v1alpha1` write can
still land an object that a `v1beta1` write would refuse, and the controller
will read it back through the hub as though it were valid.

That is a property of the window, not a defect in it, and it argues for
closing the window rather than for widening the rules — **with one exception,
and it is worth understanding why it had to be an exception.**

`FrameUser.status.passwordHash` is a `v1beta1` guarantee that could not be
left to the window. The field is credential material, and the whole point of
moving it onto `status` (F11) was that `patch frameusers` should no longer set
anyone's password. RBAC has no version dimension: `resources: [frameusers]`
covers every served version. So the paragraph above applied to it exactly as
it applies to a `maxLength` — a `v1alpha1` write of `spec.passwordHash`
travelled through conversion into `status.passwordHash` and was stored — and
the consequence was not an out-of-bounds string but any account's password,
set by anyone holding the editor tier. Worse, a plain `kubectl replace` at
`v1alpha1` that simply *omitted* the field erased the credential, needing no
ill intent and surfacing as a 401 much later.

The generic remedy — "move to `v1beta1`" — cannot work for a security
property, because the attacker chooses the version. So this one is enforced at
admission instead: the FrameUser validating webhook rejects any main-resource
write, at either version, that changes `status.passwordHash`, and
`matchPolicy: Equivalent` is what brings the `v1alpha1` request to it. Only
the `v1beta1` `frameusers/status` subresource can write the field.

The lesson generalises even if the mechanism does not: a bound that exists for
tidiness can wait for the window to close; a bound that exists for safety
needs a webhook, because schema validation is version-scoped and RBAC is not.
Nothing else `v1beta1` added is in the second category today.

It is also why the
same asymmetry bit in the other direction during the freeze: the apiserver
also declines to re-default conversion output, which made a `v1alpha1` status
patch on SchedulingPolicy evaluate a CEL rule against an absent
`spec.preemption` key. Every CEL rule in `api/` was surveyed for unguarded
scalar dereferences after that; `preemption` was the only one.

---

## 4. What is still not covered

- **There is no published, versioned chart yet.** `Chart.yaml` is at
  `version: 0.1.0`/`appVersion: 0.1.0`, and `image.repository` has no
  default because no operator image is published anywhere. "Upgrading" the
  chart today means moving between two locally built dev versions, not
  pulling a new release from a chart repository — the "one-command install
  from a published, versioned Helm chart" in the roadmap's V1 definition of
  done has not happened yet.
- **Nothing has upgraded across the freeze for real.** Section 3's ordering,
  script and rollback story are rehearsed, asserted in the Kind e2e suite,
  and unexercised on any cluster that matters. Test against a disposable
  cluster first.
- **The new API groups are not frozen.** `services.plume-labs.io` got
  `v1beta1` alongside `frame.plume-labs.io` because `FrameService` is part of
  what the freeze covers, but S2–S4 (see [roadmap.md](roadmap.md)) will
  arrive at `v1alpha1` in their own groups and this section's guarantee will
  not apply to them until Phase E.

See [roadmap.md](roadmap.md) — Phase D's exit criteria ("a tagged `v1.0.0`
installs from a published chart on a fresh cluster, passes e2e, and survives
a manager failover and an upgrade from the prior release") is the bar this
document does not yet meet. Phase B's exit criteria, which closed the
schema-stability gap, is met.
