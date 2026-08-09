# frame

Helm chart for the Frame Kubernetes operator (kubebuilder v4, 8 CRDs across
`frame.plume-labs.io` and `services.plume-labs.io`, validating/defaulting
webhooks, cert-manager-issued webhook TLS). Packages the operator only — the
React control-plane UI and authd are a separate chart.

Kustomize (`make deploy` → `config/default`) remains the day-to-day
development install path. This chart is additive, not a replacement, and
renders the same resource set as `kustomize build config/default` — see
`hack/helm-parity.sh`, which fails CI if the two ever diverge.

## Two decisions worth knowing before you "fix" them

### 1. Resource names are a fixed `frame-` prefix, not derived from the release name

Every object this chart renders (`frame-controller-manager`,
`frame-webhook-service`, `frame-serving-cert`, `frame-selfsigned-issuer`, the
`frame-*` ClusterRoles, …) uses the literal `frame-` prefix, matching
kustomize's `namePrefix: frame-` in `config/default/kustomization.yaml`.

This chart deliberately does **not** use the common Helm pattern of deriving
names from `.Release.Name` (e.g. a `frame.fullname` helper that folds in the
release name). The operator is already running on a live cluster, installed
by kustomize. The migration path onto Helm is:

```
helm install --take-ownership frame charts/frame -n frame-system
```

`--take-ownership` adopts existing objects by name — if this chart's names
depended on the release name, a release named anything other than exactly
the right string would silently produce a *different* set of objects instead
of adopting the ones already running. Fixing the names removes that failure
mode entirely: install as `helm install frame ...` (or any release name —
it's cosmetic) and you always get `frame-controller-manager`.

Do not "generalize" `templates/_helpers.tpl`'s `frame.name` into a
release-name-aware fullname helper. If this chart is ever forked to run
multiple independent copies of Frame in one cluster, that's a deliberate,
separate change requiring a real migration plan — not a refactor.

### 2. CRDs are rendered from `templates/`, not Helm's `crds/` directory

Helm's special top-level `crds/` directory is installed once on
`helm install` and **never touched again** by `helm upgrade` — by design,
Helm won't touch cluster-scoped objects it can't safely diff/own across
releases. That's the wrong tradeoff here: Frame's Phase B roadmap is CRD
schema evolution (new versions, new printer columns, tightened validation),
and an upgrade path that can't ship a CRD change isn't a real upgrade path.

So this chart's CRDs live at `files/crds/*.yaml` (a plain data directory —
note it is *not* named `crds/`, precisely to stay out of Helm's magic
directory) and are rendered by `templates/crds.yaml` via `.Files.Glob`,
gated by `.Values.crds.install` (default `true`). `helm upgrade` therefore
does apply CRD changes.

The tradeoff this creates: `helm uninstall` would, by default, delete
CRDs it manages, and Kubernetes cascade-deletes every CR of a deleted CRD.
That's why `templates/crds.yaml` stamps every CRD with
`helm.sh/resource-policy: keep` — Helm skips resources carrying that
annotation on delete, so `helm uninstall` never takes a user's CRs (or the
CRDs themselves) down with it. Removing CRDs is a deliberate, separate
`kubectl delete crd` step, never a side effect of uninstalling the operator.

`files/crds/*.yaml` are verbatim copies of `config/crd/bases/*.yaml`, kept in
sync by `make helm-sync-crds` (wired into `make manifests`, so running
`controller-gen` can't leave the chart stale) and checked in CI by
`make helm-crds-check`, which fails the build if the two have drifted.
**Never hand-edit `files/crds/*.yaml`** — `templates/crds.yaml` reads them
with `.Files.Glob` and injects the `resource-policy` annotation in the
template itself, specifically so the copies stay byte-for-byte identical to
`config/crd/bases` and diffable.

## Values

See `values.yaml` for the full, commented list. Highlights:

| Value | Default | Notes |
|---|---|---|
| `image.repository` | `""` | **Required — no default.** There is no published operator image yet; the Deployment template wraps this in `required` so a missing value fails at install time with an actionable message instead of installing and sitting in `ImagePullBackOff`. Build with `Dockerfile.controller`, **not** the root `Dockerfile` (that's the React UI). |
| `image.tag` / `image.pullPolicy` | `""` (→ `.Chart.AppVersion`) / `IfNotPresent` | |
| `replicaCount` | `1` | `cmd/main.go` always runs with `--leader-elect`; `2` is a supported, tested configuration — exactly one replica holds the Lease at a time. Ships with soft (`preferred`) pod anti-affinity by default so 2 replicas actually spread across nodes when possible, without blocking scheduling on a single-node cluster. |
| `podDisruptionBudget.enabled` / `.minAvailable` | `false` / `1` | Opt-in only: with the chart's own default `replicaCount: 1`, a PDB requiring `minAvailable: 1` can never be satisfied while evicting the only pod, which would block node drains/upgrades forever. Only turn this on alongside `replicaCount > 1`. |
| `crds.install` | `true` | See decision #2 above. |
| `metrics.enabled` | `true` | Gates the metrics port, its Service, its RBAC (`metrics-auth-role`/`metrics-reader`), and (with `certManager.enabled`) the metrics Certificate. |
| `metrics.secure` | `true` | `true` = HTTPS+authn/authz on `:8443` (matches kustomize); `false` = plain HTTP on `:8080`, for local debugging only. The metrics Service, the metrics NetworkPolicy rule and the ServiceMonitor endpoint all derive their port/scheme from this value (`templates/_helpers.tpl`'s `frame.metricsPort`/`frame.metricsPortName`) so they can't drift out of sync with the container's actual `--metrics-bind-address`. |
| `metrics.serviceMonitor.enabled` | `false` | Off by default to match kustomize (`config/prometheus` is commented out of `config/default`). Requires the Prometheus Operator CRDs in-cluster. |
| `webhooks.enabled` | `true` | Disabling it sets `ENABLE_WEBHOOKS=false` on the container (the actual switch `cmd/main.go` checks — `webhooks.enabled: false` used to only drop the cert mount/volume/port and leave webhook registration itself on, which crash-looped the manager on the now-missing `tls.crt`). With it `false`, CRs are accepted with no defaulting or validation — only meant for constrained debugging. |
| `webhooks.certSecretName` | `webhook-server-cert` | The Secret the manager mounts at `/tmp/k8s-webhook-server/serving-certs`, keys `tls.crt` / `tls.key`. |
| `webhooks.caBundle` | `""` | Only used when `certManager.enabled: false` — base64 PEM CA bundle injected into both WebhookConfigurations' `clientConfig.caBundle`. **Required whenever `certManager.enabled: false` and `webhooks.enabled: true`**: `templates/webhookconfigurations.yaml` fails the render if it's left empty, because an empty `caBundle` installs cleanly and then fails every CR create/update cluster-wide with an x509 error (`failurePolicy: Fail` on all 10 webhooks) — a silent, fail-closed outage is exactly what this guard turns into a loud, install-time one. |
| `certManager.enabled` | `true` | When `false`, the chart renders no Issuer/Certificate and expects `webhooks.certSecretName` to already exist with a cert whose CA matches `webhooks.caBundle`, provisioned by whatever your cluster uses instead (manual, `openssl`, a different PKI operator, …). |
| `networkPolicy.enabled` | `false` | Matches kustomize (`config/network-policy` is commented out of `config/default`). **The webhook rule is open to any source on port 9443, by design — this is not a partial fix, it is the honest answer.** The port is corrected from kustomize's `443` (the Service port; NetworkPolicy `ingress.ports[].port` is a pod port, and the webhook server only ever listens on 9443 — kustomize's own copy of this rule has this bug too). But restricting *source* is a different problem: on every real target (k3s, kubeadm, …) `kube-apiserver` is a host process, not a pod, so a `namespaceSelector` can never match it — verified cross-node on Kind+Calico: even with the port fixed, a `namespaceSelector`-restricted rule DROPPED (timed out) genuine host→pod admission traffic; the open rule below did not, and a real `kubectl apply` through the actual apiserver was admitted. `failurePolicy: Fail` on all 10 webhooks makes a rule the apiserver can't satisfy a cluster-wide CR write outage, not a source restriction, so this chart does not ship one by default. Set `networkPolicy.webhookSourceCIDRs` (your control-plane node IPs — cluster-specific, no safe chart default) to tighten it. |
| `rbac.tierRoles.install` | `true` | The 21 viewer/editor/admin ClusterRoles kubebuilder scaffolds per CRD — not used by the manager itself, convenience roles for cluster admins. |

## Installing without cert-manager

If `certManager.enabled: false`, before `helm install`:

1. Create a TLS Secret named `webhooks.certSecretName` (default
   `webhook-server-cert`) in the release namespace, with keys `tls.crt` and
   `tls.key`, whose DNS names cover
   `frame-webhook-service.<namespace>.svc` and
   `frame-webhook-service.<namespace>.svc.cluster.local`.
2. Set `webhooks.caBundle` to the base64-encoded PEM CA bundle that signed
   that certificate, so the API server trusts the manager's webhook
   endpoint. **This is enforced, not just documented**: leaving it empty
   fails the `helm install`/`helm template` render outright rather than
   installing into a cluster-wide admission outage.

## Anti-drift check

`hack/helm-parity.sh` renders both `helm template` and
`kustomize build config/default`, and diffs the sorted `kind|namespace|name`
sets. It fails if either side grows a resource the other one lacks. The only
allowed one-directional difference in that pass is the chart's `Namespace`
omission (decision #1's namespace-supplied-externally model).

Once the sets are confirmed identical, a second pass diffs every shared
resource's *full body* (not just its name), because a name-only check cannot
see a dropped ClusterRole verb, a dropped `--leader-elect`, or a changed
probe port — all of which would break a `--take-ownership` migration
silently. Three narrow, permanent exceptions are allow-listed explicitly in
the script: `CustomResourceDefinition` (verified separately, see decision #2
and `make helm-crds-check`), and on the `frame-controller-manager` Deployment
and `frame-metrics-certs` Certificate, the specific fields already documented
above and in `values.yaml` (`image`/`imagePullPolicy`, the default
`affinity`, and `dnsNames`).

A third pass turns on `networkPolicy.enabled`, `metrics.serviceMonitor.enabled`
and `podDisruptionBudget.enabled`, and asserts that *exactly* the four
expected extra resources (`frame-allow-metrics-traffic`,
`frame-allow-webhook-traffic`, `frame-controller-manager-metrics-monitor`,
`frame-controller-manager` PodDisruptionBudget — allow-listed by full
`kind|namespace|name`, not just kind) appear, then diffs the three that have a
`config/` counterpart against `kustomize build config/network-policy` /
`config/prometheus` (both build standalone); the PodDisruptionBudget has no
kustomize equivalent, so it gets a shape assertion (`minAvailable`, selector)
instead. Allow-listing by kind alone used to mean the chart could rename,
mis-namespace or stop rendering any of these entirely and the script would
still print `OK`; allow-listing by exact identity plus a presence assertion
closes both holes, and the content diff catches anything that renders but
drifts semantically. Two expected exceptions are asserted explicitly rather
than silently ignored: the webhook rule's port (see the `networkPolicy.enabled`
row above) and its `from` — the chart's copy must have none by default (open
to any source), where kustomize's has a `namespaceSelector` that can never
match a host-process apiserver.

The extractor (`kind`/`namespace`/`name` and full-body JSON) is a small Go
program the script writes to a temp file and runs with `go run` — deliberately
not PyYAML: this repo already needs the Go toolchain to build anything, and
`k8s.io/apimachinery` + `sigs.k8s.io/yaml` are already `go.sum` dependencies,
so there's no new dependency and no `pip install` running under `set -e` in a
CI gate.

Run it locally with `make helm-parity`; CI runs it via
`.github/workflows/lint.yml`, alongside `make helm-crds-check` (which uses
`git status --porcelain`, not `git diff`, specifically so a newly *added*
CRD file — not just a modified or deleted one — fails the build if it isn't
also synced into the chart).
