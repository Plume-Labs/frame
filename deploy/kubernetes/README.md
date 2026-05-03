# Kubernetes Manifests

This directory contains Kubernetes manifests for deploying the Cluster Control monitoring UI.

## Structure

```
kubernetes/
├── base/                    # Base manifests (environment-agnostic)
│   ├── namespace.yaml
│   ├── deployment.yaml
│   ├── service.yaml
│   ├── ingress.yaml
│   ├── hpa.yaml
│   ├── pdb.yaml
│   └── kustomization.yaml
└── overlays/               # Environment-specific overlays
    ├── development/
    │   ├── kustomization.yaml
    │   └── deployment-patch.yaml
    └── production/
        ├── kustomization.yaml
        ├── deployment-patch.yaml
        └── ingress-patch.yaml
```

## Usage

### Deploy to Development

```bash
kubectl apply -k kubernetes/overlays/development/
```

### Deploy to Production

```bash
kubectl apply -k kubernetes/overlays/production/
```

### View Generated Manifests

```bash
kubectl kustomize kubernetes/overlays/production/
```

## Configuration

### Base Configuration

The base manifests define:
- Namespace: `cluster-control`
- Deployment with 3 replicas
- ClusterIP Service on port 80
- HorizontalPodAutoscaler (3-10 replicas)
- PodDisruptionBudget (minimum 2 available)
- Ingress with TLS

### Development Overlay

- 1 replica
- Reduced resource requests/limits
- Debug logging enabled

### Production Overlay

- 5 replicas
- Increased resource requests/limits
- Production ingress hostname
- Production-grade monitoring

## Accessing the Application

### Port Forward (Local Development)

```bash
kubectl port-forward -n cluster-control svc/cluster-control-ui 8080:80
```

Visit: http://localhost:8080

### Ingress (Production)

Update the ingress hostname in `overlays/production/ingress-patch.yaml` and access via:
https://cluster.production.example.com
