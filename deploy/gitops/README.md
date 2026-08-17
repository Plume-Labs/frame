# GitOps Configuration

This directory contains GitOps configurations for both Flux CD and ArgoCD.

## Flux CD

Flux is a GitOps operator that automatically applies the contents of a Git repository to a Kubernetes cluster.

### Bootstrap Flux

```bash
cd gitops
./bootstrap-flux.sh
```

This will install Flux controllers and bootstrap it against this repo — it then
reconciles whatever Kustomizations live under `clusters/${CLUSTER_NAME}`.

Flux manages the cluster add-ons that aren't part of the
Neura/cluster-control application stack: `kmod-rdma-loader` (see
`flux/kustomizations/`).

`ksm-tuner` and `node-feature-discovery` used to be here too. `ksm-tuner` is
superseded by the Frame node-tuning agent and its manifest is gone;
`node-feature-discovery` is now a resource of `deploy/kubernetes/base`, which
Argo CD reconciles through `overlays/production`, so keeping a Flux
Kustomization for it would have made two GitOps controllers owners of one
HelmRelease. On a cluster where those two Flux Kustomizations were applied,
delete them — both carry `prune: true`, so deleting the Kustomization also
deletes what it deployed, which is what you want for `ksm-tuner` (it is the
`kubectl delete ds -n kube-system ksm-tuner` the agent's rollout requires) and
what Argo CD immediately re-creates for `node-feature-discovery`:

```bash
kubectl delete kustomization -n flux-system ksm-tuner node-feature-discovery
```

One more object has to be deleted by hand, for a different reason.
`nvidia-mps` is now rendered by `deploy/kubernetes/base`, which stamps the
kustomization's common labels into its DaemonSet selector — and a selector is
immutable. If it was ever applied by hand (`kubectl apply -f
deploy/kubernetes/base/nvidia-mps.yaml`, as its own header suggests), the
first reconcile fails with "field is immutable" until it is removed:

```bash
kubectl delete ds -n kube-system nvidia-mps
```

**cluster-control-ui and the controller-manager are Argo CD-managed** (see the
ArgoCD section below, `argocd/applications/frame.yaml`) — they used to have
their own Flux Kustomization + image-automation setup too, but that duplicated
the Argo CD Application for no reason and was removed.

### Manual Flux Installation

```bash
flux install --namespace flux-system
```

### Apply Flux Resources

```bash
kubectl apply -f flux/sources/git-repository.yaml
kubectl apply -f flux/kustomizations/kmod-rdma-loader.yaml
```

### Monitor Flux

```bash
flux get all
flux logs
```

## ArgoCD

ArgoCD is a declarative GitOps continuous delivery tool for Kubernetes.

### Install ArgoCD

```bash
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
```

### Apply ArgoCD Applications

```bash
kubectl apply -f argocd/projects/cluster-infrastructure.yaml
kubectl apply -f argocd/applications/frame.yaml
```

### Access ArgoCD UI

```bash
kubectl port-forward svc/argocd-server -n argocd 8080:443
```

Get admin password:
```bash
kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | base64 -d
```

Visit: https://localhost:8080

## Flux vs ArgoCD

### Use Flux if:
- You prefer CLI-first workflows
- You want automatic image updates from registries
- You need multi-tenancy with multiple Git sources

### Use ArgoCD if:
- You prefer UI-first workflows
- You want better visualization of application state
- You need SSO integration (OIDC, SAML)

## Continuous Deployment Flow

```
┌─────────────┐
│  Developer  │
└──────┬──────┘
       │ git push
       ▼
┌─────────────┐
│  Git Repo   │
└──────┬──────┘
       │
       ├──────────────┐
       │              │
       ▼              ▼
┌─────────┐    ┌──────────┐
│  Flux   │    │  ArgoCD  │
└────┬────┘    └─────┬────┘
     │               │
     └───────┬───────┘
             ▼
      ┌────────────┐
      │ Kubernetes │
      └────────────┘
```
