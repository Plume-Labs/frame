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

Flux manages a handful of cluster add-ons that aren't part of the
Neura/cluster-control application stack: `ksm-tuner`, `node-feature-discovery`,
`kmod-rdma-loader` (see `flux/kustomizations/`). **cluster-control-ui and the
controller-manager are Argo CD-managed** (see the ArgoCD section below,
`argocd/applications/frame.yaml`) — they used to have their own Flux
Kustomization + image-automation setup too, but that duplicated the Argo CD
Application for no reason and was removed.

### Manual Flux Installation

```bash
flux install --namespace flux-system
```

### Apply Flux Resources

```bash
kubectl apply -f flux/sources/git-repository.yaml
kubectl apply -f flux/kustomizations/ksm-tuner.yaml
kubectl apply -f flux/kustomizations/node-feature-discovery.yaml
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
