# Terraform — Cluster Control Provisioning

Terraform configuration for provisioning Cluster Control resources on a local Kubernetes cluster (bare-metal or kind/k3d).

## Prerequisites

- Terraform ≥ 1.6
- A running Kubernetes cluster with `kubectl` access
- `KUBECONFIG` set or a kubeconfig file at `~/.kube/config`

## Quick Start

```bash
cd deploy/terraform

# Initialise providers
terraform init

# Preview changes
terraform plan

# Apply
terraform apply
```

After `apply`, access the UI with:

```bash
kubectl port-forward -n cluster-control svc/cluster-control-ui 8080:80
# then open http://localhost:8080
```

## Variables

| Variable           | Default                        | Description                              |
|--------------------|--------------------------------|------------------------------------------|
| `kubeconfig_path`  | `~/.kube/config`               | Path to kubeconfig                       |
| `kube_context`     | *(current context)*            | Kubernetes context to use                |
| `namespace`        | `cluster-control`              | Namespace for all resources              |
| `image_repository` | `ghcr.io/plume-labs/frame`     | Container image repository               |
| `image_tag`        | `latest`                       | Image tag to deploy                      |
| `replicas`         | `1`                            | Number of UI replicas                    |
| `node_env`         | `production`                   | `development` or `production`            |
| `resources`        | 256Mi/100m req, 512Mi/500m lim | Container resource requests and limits   |

## Example: deploy a specific version

```bash
terraform apply \
  -var="image_tag=v1.2.0" \
  -var="replicas=2"
```

## Resources managed

- `kubernetes_namespace` — `cluster-control`
- `kubernetes_service_account` — `cluster-control-ui`
- `kubernetes_cluster_role` — `cluster-control-viewer` (read-only access to nodes, pods, deployments, metrics)
- `kubernetes_cluster_role_binding` — binds the service account to the role
- `kubernetes_deployment` — UI pods with liveness/readiness probes and non-root security context
- `kubernetes_service` — ClusterIP service on port 80 → 8080

## Upgrading

To roll out a new image tag without changing anything else:

```bash
terraform apply -var="image_tag=v1.3.0"
```

Terraform will perform an in-place update to the Deployment, triggering a rolling restart.
