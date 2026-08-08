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
