# Deployment

---

## Prerequisites

- Docker (to build the UI image)
- `kubectl` 1.28+ with cluster access
- `kustomize` 5.0+ (or use `kubectl kustomize`)
- cert-manager installed on the target cluster (required for webhook TLS)

---

## 1. Build and push the UI image

The Dockerfile builds the React UI with Vite and serves it via nginx:

```bash
# Default: tags as controller:latest
make docker-build

# With a custom registry
make docker-build docker-push IMG=ghcr.io/<your-org>/frame-ui:v0.1.0
```

Or directly with Docker:

```bash
docker build -t ghcr.io/<your-org>/frame-ui:dev .
docker push ghcr.io/<your-org>/frame-ui:dev
```

---

## 2. Install the operator (CRDs + controller + webhooks)

```bash
# Full install: CRDs, RBAC, controller deployment, webhooks, cert-manager integration
kubectl apply -k config/default
```

This applies the `frame-system` namespace, all eight CRDs (across `frame.plume-labs.io` and `services.plume-labs.io`), RBAC (ClusterRoles + bindings), the controller manager deployment, webhook configuration, and cert-manager certificate resources.

Verify:

```bash
kubectl -n frame-system get pods
# NAME                                         READY   STATUS    RESTARTS   AGE
# frame-controller-manager-<hash>              2/2     Running   0          30s

kubectl get crds | grep frame
# framejobs.frame.plume-labs.io
# framenodes.frame.plume-labs.io
# ...
```

---

## 3. Deploy the UI

### Development overlay

Single replica, debug logging, `ENV=development`:

```bash
kustomize build deploy/kubernetes/overlays/development | kubectl apply -f -
```

Update the image tag in `deploy/kubernetes/overlays/development/kustomization.yaml`:

```yaml
images:
  - name: cluster-control
    newName: ghcr.io/<your-org>/frame-ui
    newTag: dev
```

### Production overlay

```bash
kustomize build deploy/kubernetes/overlays/production | kubectl apply -f -
```

Update `deploy/kubernetes/overlays/production/kustomization.yaml` with your registry and tag before applying.

---

## 4. Configure the UI for in-cluster auth

In production, the UI needs a ServiceAccount token to call the K8s API. Two options:

### Option A — Projected ServiceAccount token (recommended)

Mount a projected token into the nginx pod and serve it as a small config endpoint, or inject it at build time via a ConfigMap.

### Option B — Static token in ConfigMap

Create a long-lived token for a `frame-editor` or `frame-viewer` ServiceAccount and inject it via an init container or nginx config that sets `window.__FRAME_TOKEN__`.

```bash
# Create a long-lived SA token (K8s 1.24+)
kubectl create token frame-ui-sa -n cluster-control --duration=8760h
```

The UI reads `window.__FRAME_TOKEN__` on startup. Set it before the `<script>` that loads the app bundle.

---

## 5. Ingress

The base kustomization includes an `Ingress` resource in `deploy/kubernetes/base/ingress.yaml`. Edit the host to match your cluster's ingress controller:

```yaml
spec:
  rules:
    - host: frame.your-cluster.example.com
```

Or use a port-forward for quick access:

```bash
kubectl -n cluster-control port-forward svc/cluster-control-ui 8080:8080
# → http://localhost:8080
```

---

## 6. Cert-manager (required for webhooks)

The operator's validating/defaulting webhooks require TLS managed by cert-manager. If cert-manager is not already installed:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=Available deployment --all --timeout=120s
```

Then install the operator:

```bash
kubectl apply -k config/default
```

---

## Installing the operator via Helm

Alternative to step 2 above, for the operator only. The chart at
`charts/frame/` packages the CRDs, the controller Deployment, RBAC, the
webhook configuration, and (optionally) the cert-manager `Issuer`/
`Certificate` that issues the webhook's TLS. **It does not install the UI or
authd** — those still come from `deploy/kubernetes/` via kustomize (steps 3-5
above). Read `charts/frame/README.md` before changing anything in the chart:
it documents two decisions (fixed `frame-` resource names instead of the
usual release-name-derived ones, and CRDs rendered from `templates/` instead
of Helm's `crds/` directory) that look like bugs and are not.

If you already have the operator running from `kubectl apply -k config/default`
and want to move it onto this chart instead of doing a fresh install, see
[upgrading.md](upgrading.md) — that migration has its own procedure and its
own caveats.

### Prerequisites

- A Kubernetes cluster, 1.29+ (`Chart.yaml`'s `kubeVersion` gate). Verified
  in this session against a kind `v1.34.0` cluster and (read-only, via
  `helm template`/`--dry-run`) against the k3s `v1.36.2+k3s1` test cluster.
- Helm — verified with v4.2.3 locally; the chart pins no minimum Helm
  version.
- cert-manager installed on the target cluster. The chart's webhooks depend
  on it by default (`certManager.enabled: true`): it issues the webhook
  serving certificate and injects the CA into both `WebhookConfiguration`s.
  See the "Cert-manager" section above for the install command. Running
  without cert-manager is possible (`certManager.enabled: false`) but shifts
  certificate issuance onto you — see "Installing without cert-manager" in
  `charts/frame/README.md` before choosing that path.
- A built operator image (`Dockerfile.controller`, **not** the root
  `Dockerfile`, which builds the UI). There is no published image yet, so
  `image.repository` has no default — the chart's `required` guard fails the
  install with an actionable message rather than installing into
  `ImagePullBackOff`.

### Install

```bash
helm install frame charts/frame \
  --namespace frame-system --create-namespace \
  --set image.repository=<your-registry>/frame-controller \
  --set image.tag=<tag>
```

The release name (`frame` above) is cosmetic — every object this chart
renders uses a fixed `frame-` prefix regardless of it, so any release name
produces the same `frame-controller-manager`, `frame-webhook-service`, etc.
(`charts/frame/README.md` explains why this is deliberate). This exact
invocation, with a real image swapped in, is what was run end-to-end
(install → CRDs present → cert-manager issues the cert → `helm upgrade` →
`helm uninstall`) against a disposable local kind cluster while writing this
guide.

### Values worth knowing on a first install

| Value | Default | Why it matters here |
|---|---|---|
| `image.repository` | `""`, **required** | No published image exists yet; must point at an image built from `Dockerfile.controller`. |
| `replicaCount` | `1` | `2` is a supported HA configuration (leader election is always on) — see the runbook's "Failover" section for measured takeover behaviour. |
| `crds.install` | `true` | Set `false` only if CRDs are managed some other way; the chart fails the render if `true` and no CRD files are found, so this is not a silent no-op. |
| `certManager.enabled` | `true` | `false` means you provision `webhooks.certSecretName` and `webhooks.caBundle` yourself — see "Installing without cert-manager" in `charts/frame/README.md`. |
| `metrics.serviceMonitor.enabled` | `false` | Turn on only if the Prometheus Operator CRDs are already installed. |
| `networkPolicy.enabled` | `false` | Off by default, matching kustomize. If enabled, the webhook rule is intentionally open on port 9443 to any source — `charts/frame/README.md` explains why a source-restricted rule breaks admission on real clusters. |
| `rbac.tierRoles.install` | `true` | The 21 viewer/editor/admin convenience `ClusterRole`s per CRD; not required by the manager itself. |

The full, commented list is in `charts/frame/values.yaml`; `charts/frame/README.md`
has the complete table with the reasoning behind each default.

### Verifying the install

```bash
kubectl -n frame-system rollout status deployment/frame-controller-manager
kubectl get crd | grep plume-labs.io
kubectl -n frame-system get certificate frame-serving-cert   # if certManager.enabled
```

All three are printed by the chart's own `NOTES.txt` after `helm install`
completes. For anything beyond "did the install come up" — leader lease,
certificate expiry, webhook CA match, the single dry-run command that
exercises the whole admission path — see the "Is it healthy?" section of
[runbook.md](runbook.md), which was written and measured against a running
cluster and is not repeated here.

### Uninstalling

```bash
helm uninstall frame -n frame-system
```

Every CRD this chart installs carries `helm.sh/resource-policy: keep`, so
`helm uninstall` removes the Deployment, RBAC and webhook configuration but
**leaves the CRDs (and any CRs) in place** — verified in this session: after
`helm uninstall` on a disposable kind cluster, all eight `plume-labs.io` CRDs
were still present and `helm uninstall` printed each one under "kept due to
the resource policy." Removing the CRDs themselves is a deliberate, separate,
manual step (`kubectl delete crd <name>`) — not exercised here, since on a
real cluster it cascade-deletes every CR of that kind. See "CRDs are
rendered from `templates/`, not Helm's `crds/` directory" in
`charts/frame/README.md` for why the chart is built this way.

---

## Kustomize overlay reference

```
deploy/kubernetes/
├── base/
│   ├── namespace.yaml
│   ├── rbac.yaml
│   ├── deployment.yaml
│   ├── service.yaml
│   ├── ingress.yaml
│   ├── hpa.yaml
│   └── pdb.yaml
└── overlays/
    ├── development/      # 1 replica, debug logging
    └── production/       # production replica count, image tag
```

---

## Makefile targets

| Target | What it does |
|---|---|
| `make docker-build` | Build the controller image |
| `make docker-push` | Push the controller image |
| `make install` | Apply CRDs to the cluster |
| `make deploy` | Apply the full operator (config/default) |
| `make undeploy` | Remove the operator from the cluster |
| `make setup-test-e2e` | Create a Kind cluster for e2e tests |
| `make test-e2e` | Run e2e tests against the Kind cluster |
