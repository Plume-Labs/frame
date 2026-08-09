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
| `image.repository` / `image.tag` / `image.pullPolicy` | `example.com/frame` / `""` (→ `.Chart.AppVersion`) / `IfNotPresent` | Build with `Dockerfile.controller`, **not** the root `Dockerfile` (that's the React UI). |
| `replicaCount` | `1` | `cmd/main.go` always runs with `--leader-elect`; `2` is a supported, tested configuration — exactly one replica holds the Lease at a time. |
| `crds.install` | `true` | See decision #2 above. |
| `metrics.enabled` | `true` | Gates the `:8443` metrics port, its Service, its RBAC (`metrics-auth-role`/`metrics-reader`), and (with `certManager.enabled`) the metrics Certificate. |
| `metrics.serviceMonitor.enabled` | `false` | Off by default to match kustomize (`config/prometheus` is commented out of `config/default`). Requires the Prometheus Operator CRDs in-cluster. |
| `webhooks.enabled` | `true` | Disabling it means CRs are accepted with no defaulting or validation — only meant for constrained debugging. |
| `webhooks.certSecretName` | `webhook-server-cert` | The Secret the manager mounts at `/tmp/k8s-webhook-server/serving-certs`, keys `tls.crt` / `tls.key`. |
| `webhooks.caBundle` | `""` | Only used when `certManager.enabled: false` — base64 PEM CA bundle injected into both WebhookConfigurations' `clientConfig.caBundle`. |
| `certManager.enabled` | `true` | When `false`, the chart renders no Issuer/Certificate and expects `webhooks.certSecretName` to already exist with a cert whose CA matches `webhooks.caBundle`, provisioned by whatever your cluster uses instead (manual, `openssl`, a different PKI operator, …). |
| `networkPolicy.enabled` | `false` | Matches kustomize (`config/network-policy` is commented out of `config/default`). |
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
   endpoint.

## Anti-drift check

`hack/helm-parity.sh` renders both `helm template` and
`kustomize build config/default`, and diffs the sorted `kind|namespace|name`
sets. It fails if either side grows a resource the other one lacks. The only
allowed one-directional differences are documented inline in the script:
the chart's `Namespace` omission (decision #1's namespace-supplied-externally
model) and the opt-in `ServiceMonitor`/`NetworkPolicy` resources kustomize
has commented out of `config/default`. Run it locally with
`make helm-parity`; CI runs it via `.github/workflows/lint.yml`.
