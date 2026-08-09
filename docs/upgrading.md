# Upgrading

Three different things get called "upgrading" here, and they don't share a
procedure: moving today's kustomize install onto the Helm chart, moving from
one chart version to the next, and — the part with no procedure yet —
upgrading across a schema change once the API stops being `v1alpha1`. Each
gets its own section below.

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
here. No CRD schema has actually changed since the chart shipped (there is
only one version, `v1alpha1`, of everything), so this describes the
mechanism the chart provides, not a schema migration that has been
exercised end-to-end.

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

## 3. What is not covered yet

Frame's API is `v1alpha1` and explicitly unfrozen — see
[roadmap.md](roadmap.md), Phase B, which is gated on the S1 service catalog
and has not started. `v1beta1` with a conversion webhook, a documented
storage version, and a real deprecation/migration policy are all Phase B
deliverables, not shipped.

Concretely, for someone upgrading the chart today:

- **There is no compatibility guarantee across chart versions.** A future
  chart release can change a CRD field's name, type, or validation without
  a conversion path, because there is no second API version yet for
  anything to convert between. `helm upgrade` will apply that change
  mechanically (see the CRD section above) — it will not warn you that the
  change is breaking, because Helm has no way to know that and this
  project has not yet built the layer that would (that layer is `v1beta1` +
  the conversion webhook, per Phase B).
- **There is no published, versioned chart yet.** `Chart.yaml` is at
  `version: 0.1.0`/`appVersion: 0.1.0`, and `image.repository` has no
  default because no operator image is published anywhere. "Upgrading" the
  chart today means moving between two locally built dev versions, not
  pulling a new release from a chart repository — the "one-command install
  from a published, versioned Helm chart" in the roadmap's V1 definition of
  done has not happened yet.
- **What to expect instead, until Phase B ships:** read the diff between the
  chart versions (or CRD YAML under `config/crd/bases/`) before upgrading,
  same as you would for any other pre-1.0 CRD-based project. Test the
  upgrade against a disposable cluster first if the CRD diff touches a field
  your CRs actually use. There is no tooling here yet that does this for
  you.

See [roadmap.md](roadmap.md) — Phase D's exit criteria ("a tagged `v1.0.0`
installs from a published chart on a fresh cluster, passes e2e, and survives
a manager failover and an upgrade from the prior release") is the bar this
document does not yet meet, and Phase B's exit criteria is what closes the
schema-stability gap above.
