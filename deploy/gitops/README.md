# GitOps Configuration

This directory contains GitOps configurations for both Flux CD and ArgoCD.

## Flux CD

Flux is a GitOps operator that automatically applies the contents of a Git repository to a Kubernetes cluster.

### Bootstrap Flux

```bash
cd gitops
./bootstrap-flux.sh
```

This will:
1. Install Flux controllers
2. Create GitRepository source
3. Create Kustomization for cluster-control
4. Enable image automation

### Manual Flux Installation

```bash
flux install --namespace flux-system
```

### Apply Flux Resources

```bash
kubectl apply -f flux/sources/git-repository.yaml
kubectl apply -f flux/kustomizations/cluster-control.yaml
kubectl apply -f flux/image-automation.yaml
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
kubectl apply -f argocd/applications/cluster-control.yaml
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

## Image Update Automation

Flux can automatically update images when new versions are pushed to the container registry.

The `image-automation.yaml` file configures:
1. ImageRepository - watches the container registry
2. ImagePolicy - defines version constraints
3. ImageUpdateAutomation - commits updates to Git

New images matching the semver policy will be automatically deployed.
