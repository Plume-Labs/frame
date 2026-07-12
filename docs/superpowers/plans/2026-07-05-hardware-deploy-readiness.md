# Hardware Deploy Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Frame deployable on real bare-metal hardware — from image build to a running UI with working operator, on a cluster bootstrapped from scratch.

**Architecture:** Six independent blockers addressed in dependency order: image pipeline first (everything else pulls the image), then cluster pre-reqs, then TLS/networking, then UI auth, then Talos secrets, then GitOps. FrameResourceQuota is already implemented — not a blocker.

**Tech Stack:** Kustomize 5, cert-manager, Argo Workflows, ingress-nginx, Talos Linux gRPC, Kubernetes projected ServiceAccount tokens, nginx.

## Global Constraints

- Kubernetes 1.28+
- cert-manager required for webhook TLS — operator won't start without it
- All kustomize image tags set via `kustomize edit set image` — never hardcode registry in committed files
- Talos Secret keys must be exactly: `ca.crt`, `client.crt`, `client.key`
- Frame CRD group: `frame.plume-labs.io`, version `v1alpha1`
- UI SA: `cluster-control-ui` in namespace `cluster-control`
- Operator namespace: `frame-system`

---

## File map

| File | Change |
|---|---|
| `Makefile` | Add `IMG_UI` variable for UI image, separate from operator `IMG` |
| `deploy/scripts/bootstrap-prereqs.sh` | New — installs cert-manager, Argo, ingress-nginx, Volcano |
| `deploy/kubernetes/base/kustomization.yaml` | Remove placeholder `newName` — set via overlay only |
| `deploy/kubernetes/overlays/development/kustomization.yaml` | Remove hardcoded `your-org` placeholder |
| `deploy/kubernetes/overlays/production/kustomization.yaml` | Remove hardcoded `your-org` placeholder |
| `deploy/kubernetes/base/ingress.yaml` | Parameterize hostname via kustomize var |
| `deploy/kubernetes/overlays/development/kustomization.yaml` | Set dev hostname |
| `deploy/kubernetes/overlays/production/ingress-patch.yaml` | Set prod hostname |
| `deploy/certmanager/cluster-issuer.yaml` | New — letsencrypt-prod + staging ClusterIssuer template |
| `deploy/kubernetes/base/rbac.yaml` | Add Frame CRD verbs to `cluster-control-viewer` ClusterRole |
| `deploy/kubernetes/base/deployment.yaml` | Add init container for SA token injection |
| `deploy/docker/nginx.conf` | Add `location /config.js` serving projected token |
| `deploy/kubernetes/samples/talos-client-secret.yaml` | New — Secret template with Talos TLS keys |
| `config/samples/frame_v1alpha1_talosmachineconfig.yaml` | Fill in real fields referencing the Secret |
| `config/samples/frame_v1alpha1_talosupgrade.yaml` | Fill in real fields |
| `deploy/gitops/flux/sources/git-repository.yaml` | Parameterize repo URL |
| `deploy/gitops/argocd/applications/cluster-control.yaml` | Parameterize repo URL |
| `deploy/gitops/bootstrap-flux.sh` | Use `GITHUB_OWNER`/`GITHUB_REPOSITORY` env vars (already done — verify) |

---

## Task 1: Image pipeline — parameterize registry, document push workflow

**Files:**
- Modify: `Makefile`
- Modify: `deploy/kubernetes/base/kustomization.yaml`
- Modify: `deploy/kubernetes/overlays/development/kustomization.yaml`
- Modify: `deploy/kubernetes/overlays/production/kustomization.yaml`
- Modify: `deploy/gitops/flux/image-automation.yaml`

**Context:** Two images exist — the Go operator (`IMG`, built via `make docker-build`) and the React UI (built via Dockerfile). Both have `your-org` placeholder in kustomize. The kustomize `images:` block is the right mechanism — `newName`+`newTag` should be set via `kustomize edit set image` at deploy time, not committed. The base should only define the logical image name; overlays set the real name+tag.

- [ ] **Step 1: Remove `newName`/`newTag` from base kustomization**

Edit `deploy/kubernetes/base/kustomization.yaml`. Replace:
```yaml
images:
  - name: cluster-control
    newName: ghcr.io/your-org/cluster-control
    newTag: latest
```
With:
```yaml
# Image name and tag are set per-overlay via `kustomize edit set image`.
# Run: kustomize edit set image cluster-control=<registry>/<image>:<tag>
images:
  - name: cluster-control
```

- [ ] **Step 2: Remove placeholder from dev overlay**

Edit `deploy/kubernetes/overlays/development/kustomization.yaml`. Replace:
```yaml
images:
  - name: cluster-control
    newName: ghcr.io/your-org/cluster-control
    newTag: dev
```
With:
```yaml
# Set before deploying:
#   cd deploy/kubernetes/overlays/development
#   kustomize edit set image cluster-control=<registry>/frame-ui:dev
images:
  - name: cluster-control
```

- [ ] **Step 3: Same for production overlay**

Edit `deploy/kubernetes/overlays/production/kustomization.yaml`. Same removal — replace the `images:` block with the same comment-only placeholder as in step 2 (with `:latest` in the comment example).

- [ ] **Step 4: Add `IMG_UI` to Makefile and a `push-ui` target**

Open `Makefile`. After the `IMG ?= controller:latest` line, add:

```makefile
# Image for the React UI (Dockerfile at repo root)
IMG_UI ?= frame-ui:latest

.PHONY: docker-build-ui
docker-build-ui: ## Build the Frame UI Docker image
	$(CONTAINER_TOOL) build -t $(IMG_UI) .

.PHONY: docker-push-ui
docker-push-ui: ## Push the Frame UI Docker image
	$(CONTAINER_TOOL) push $(IMG_UI)

.PHONY: set-image-ui
set-image-ui: ## Set UI image in development overlay (requires IMG_UI)
	cd deploy/kubernetes/overlays/development && kustomize edit set image cluster-control=$(IMG_UI)

.PHONY: set-image-ui-prod
set-image-ui-prod: ## Set UI image in production overlay (requires IMG_UI)
	cd deploy/kubernetes/overlays/production && kustomize edit set image cluster-control=$(IMG_UI)
```

- [ ] **Step 5: Fix `deploy/gitops/flux/image-automation.yaml` placeholder**

Replace the hardcoded `ghcr.io/your-org/cluster-control` with a comment instructing the user to set it:

```yaml
# Edit newName to match your actual registry before committing to your GitOps repo.
image: ghcr.io/REPLACE_WITH_YOUR_ORG/cluster-control
```

- [ ] **Step 6: Verify build works**

```bash
make docker-build IMG=localhost:5000/frame-operator:test
make docker-build-ui IMG_UI=localhost:5000/frame-ui:test
```
Expected: both images build without error.

- [ ] **Step 7: Commit**

```bash
git add Makefile deploy/kubernetes/ deploy/gitops/flux/image-automation.yaml
git commit -m "fix: parameterize image registry — set via kustomize edit set image, not hardcoded"
```

---

## Task 2: Cluster pre-reqs bootstrap script

**Files:**
- Create: `deploy/scripts/bootstrap-prereqs.sh`

**Context:** Before `kubectl apply -k config/default` can succeed, four things must exist on the cluster: cert-manager (for webhook TLS), Argo Workflows (for FrameJob → Workflow), ingress-nginx (for UI Ingress), and optionally Volcano (for SchedulingPolicy queue). Currently no script installs these. The operator gracefully degrades without Volcano, but the first three are hard requirements.

- [ ] **Step 1: Create the script**

Create `deploy/scripts/bootstrap-prereqs.sh`:

```bash
#!/usr/bin/env bash
# Install cluster prerequisites for Frame.
# Run once against a fresh cluster before: kubectl apply -k config/default
#
# Usage: ./bootstrap-prereqs.sh [--skip-volcano]
set -euo pipefail

SKIP_VOLCANO=false
for arg in "$@"; do
  [[ "$arg" == "--skip-volcano" ]] && SKIP_VOLCANO=true
done

for bin in kubectl helm; do
  command -v "$bin" >/dev/null 2>&1 || { echo "❌ Missing: $bin"; exit 1; }
done

echo "▶ cert-manager"
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=Available deployment --all --timeout=120s

echo "▶ ingress-nginx"
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx --force-update
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.service.type=LoadBalancer \
  --wait --timeout 120s

echo "▶ Argo Workflows"
kubectl create namespace argo --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argo -f https://github.com/argoproj/argo-workflows/releases/latest/download/install.yaml
kubectl -n argo wait --for=condition=Available deployment --all --timeout=120s

if [[ "$SKIP_VOLCANO" == "false" ]]; then
  echo "▶ Volcano (gang-scheduling)"
  helm repo add volcano-sh https://volcano-sh.github.io/helm-charts --force-update
  helm upgrade --install volcano volcano-sh/volcano \
    --namespace volcano-system --create-namespace \
    --wait --timeout 120s
fi

echo "✅ Prerequisites installed. Next: kubectl apply -k config/default"
```

- [ ] **Step 2: Make executable**

```bash
chmod +x /home/rmocq/frame/deploy/scripts/bootstrap-prereqs.sh
```

- [ ] **Step 3: Verify script is valid bash**

```bash
bash -n deploy/scripts/bootstrap-prereqs.sh
```
Expected: no output (no syntax errors).

- [ ] **Step 4: Commit**

```bash
git add deploy/scripts/bootstrap-prereqs.sh
git commit -m "feat: add bootstrap-prereqs.sh — installs cert-manager, Argo, ingress-nginx, Volcano"
```

---

## Task 3: Ingress hostname + ClusterIssuer template

**Files:**
- Create: `deploy/certmanager/cluster-issuer.yaml`
- Modify: `deploy/kubernetes/base/ingress.yaml`
- Modify: `deploy/kubernetes/overlays/production/ingress-patch.yaml`

**Context:** The Ingress uses hostname `cluster-control.example.com` (placeholder) and references a ClusterIssuer `letsencrypt-prod` that doesn't exist anywhere in the repo. Two things needed: a ClusterIssuer template users fill in with their email, and a way to set the hostname per environment without editing base files.

- [ ] **Step 1: Create ClusterIssuer template**

Create `deploy/certmanager/cluster-issuer.yaml`:

```yaml
# Replace YOUR_EMAIL with your ACME registration email before applying.
# Apply: kubectl apply -f deploy/certmanager/cluster-issuer.yaml
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-staging
spec:
  acme:
    server: https://acme-staging-v02.api.letsencrypt.org/directory
    email: YOUR_EMAIL
    privateKeySecretRef:
      name: letsencrypt-staging-key
    solvers:
      - http01:
          ingress:
            ingressClassName: nginx
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: YOUR_EMAIL
    privateKeySecretRef:
      name: letsencrypt-prod-key
    solvers:
      - http01:
          ingress:
            ingressClassName: nginx
```

- [ ] **Step 2: Parameterize base ingress hostname**

Edit `deploy/kubernetes/base/ingress.yaml`. Replace both occurrences of `cluster-control.example.com` with `REPLACE_HOSTNAME`:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: cluster-control-ingress
  namespace: cluster-control
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - REPLACE_HOSTNAME
      secretName: cluster-control-tls
  rules:
    - host: REPLACE_HOSTNAME
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: cluster-control-ui
                port:
                  name: http
```

- [ ] **Step 3: Add hostname patch to dev overlay**

Create `deploy/kubernetes/overlays/development/ingress-patch.yaml`:

```yaml
# Replace with your actual dev hostname or use port-forward instead.
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: cluster-control-ingress
  namespace: cluster-control
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-staging
spec:
  tls:
    - hosts:
        - frame-dev.REPLACE_YOUR_DOMAIN
      secretName: cluster-control-tls-dev
  rules:
    - host: frame-dev.REPLACE_YOUR_DOMAIN
```

- [ ] **Step 4: Update production ingress patch**

Edit `deploy/kubernetes/overlays/production/ingress-patch.yaml`. Replace `cluster.production.example.com` with `frame.REPLACE_YOUR_DOMAIN` — same pattern, user replaces before deploy.

- [ ] **Step 5: Add ingress patch to dev kustomization**

Edit `deploy/kubernetes/overlays/development/kustomization.yaml`. Add `ingress-patch.yaml` to `patchesStrategicMerge:` if not present:

```yaml
patchesStrategicMerge:
  - deployment-patch.yaml
  - ingress-patch.yaml
```

- [ ] **Step 6: Verify kustomize renders without error**

```bash
kustomize build deploy/kubernetes/overlays/development 2>&1 | head -30
```
Expected: valid YAML, no error about missing resources.

- [ ] **Step 7: Commit**

```bash
git add deploy/certmanager/ deploy/kubernetes/
git commit -m "fix: parameterize ingress hostname, add letsencrypt ClusterIssuer template"
```

---

## Task 4: UI production auth — SA token injection + Frame CRD RBAC

**Files:**
- Modify: `deploy/kubernetes/base/rbac.yaml`
- Modify: `deploy/kubernetes/base/deployment.yaml`
- Modify: `deploy/docker/nginx.conf`

**Context:** Two problems. (1) The `cluster-control-viewer` ClusterRole only lists core K8s resources — it has zero Frame CRD permissions, so the UI would get 403 on all `/apis/frame.plume-labs.io/…` calls. (2) In prod, `window.__FRAME_TOKEN__` must be set before the app loads. The approach: projected ServiceAccount token mounted as a volume → nginx init container writes it to `config.js` → nginx serves `config.js` → `index.html` loads it via a `<script>` tag. The SDK already reads `window.__FRAME_TOKEN__`.

- [ ] **Step 1: Add Frame CRD permissions to ClusterRole**

Edit `deploy/kubernetes/base/rbac.yaml`. Replace the existing ClusterRole `cluster-control-viewer` rules with:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-control-viewer
rules:
  # Core K8s resources
  - apiGroups: [""]
    resources: [nodes, pods, services, namespaces, resourcequotas, events]
    verbs: [get, list, watch]
  - apiGroups: [apps]
    resources: [deployments, daemonsets, statefulsets]
    verbs: [get, list, watch]
  - apiGroups: [metrics.k8s.io]
    resources: [nodes, pods]
    verbs: [get, list]
  # Frame CRDs — full read + write for operator actions
  - apiGroups: [frame.plume-labs.io]
    resources:
      - framejobs
      - framejobs/status
      - framenodes
      - framenodes/status
      - schedulingpolicies
      - schedulingpolicies/status
      - frameresourcequotas
      - frameresourcequotas/status
      - talosmachineconfigs
      - talosmachineconfigs/status
      - talosupgrades
      - talosupgrades/status
    verbs: [get, list, watch, create, update, patch, delete]
```

- [ ] **Step 2: Add projected SA token volume to deployment**

Edit `deploy/kubernetes/base/deployment.yaml`. In `spec.template.spec.volumes`, add:

```yaml
        - name: sa-token
          projected:
            sources:
              - serviceAccountToken:
                  path: token
                  expirationSeconds: 3600
                  audience: kubernetes
        - name: config-js
          emptyDir: {}
```

In `spec.template.spec.containers[0].volumeMounts`, add:

```yaml
            - name: config-js
              mountPath: /usr/share/nginx/html/config.js
              subPath: config.js
```

In `spec.template.spec`, add before `containers:`:

```yaml
      initContainers:
        - name: inject-token
          image: busybox:1.36
          command:
            - sh
            - -c
            - |
              TOKEN=$(cat /var/run/secrets/sa/token)
              echo "window.__FRAME_TOKEN__='${TOKEN}';" > /config/config.js
          volumeMounts:
            - name: sa-token
              mountPath: /var/run/secrets/sa
            - name: config-js
              mountPath: /config
```

- [ ] **Step 3: Add `config.js` location to nginx.conf**

Edit `deploy/docker/nginx.conf`. Add before the closing `}`:

```nginx
    location = /config.js {
        alias /usr/share/nginx/html/config.js;
        add_header Cache-Control "no-store";
    }
```

- [ ] **Step 4: Load config.js in index.html**

Edit `index.html`. Add before the closing `</head>`:

```html
    <!-- Injected by K8s init container in production; no-op in dev (kubectl proxy) -->
    <script src="/config.js" onerror="void 0"></script>
```

- [ ] **Step 5: Verify nginx config syntax**

```bash
docker run --rm -v $(pwd)/deploy/docker/nginx.conf:/etc/nginx/conf.d/default.conf:ro nginx:alpine nginx -t
```
Expected: `configuration file /etc/nginx/nginx.conf test is successful`

- [ ] **Step 6: Verify kustomize still builds**

```bash
kustomize build deploy/kubernetes/overlays/development 2>&1 | grep -E "error|Error" || echo "OK"
```
Expected: `OK`

- [ ] **Step 7: Commit**

```bash
git add deploy/kubernetes/base/rbac.yaml deploy/kubernetes/base/deployment.yaml \
        deploy/docker/nginx.conf index.html
git commit -m "feat: add Frame CRD RBAC to UI SA, inject K8s SA token as window.__FRAME_TOKEN__ via init container"
```

---

## Task 5: Talos Secret template + complete sample CRs

**Files:**
- Create: `deploy/kubernetes/samples/talos-client-secret.yaml`
- Modify: `config/samples/frame_v1alpha1_talosmachineconfig.yaml`
- Modify: `config/samples/frame_v1alpha1_talosupgrade.yaml`

**Context:** `TalosMachineConfig` and `TalosUpgrade` controllers build a Talos gRPC client from a K8s Secret referenced by `spec.talosSecretRef`. The Secret must contain three keys: `ca.crt`, `client.crt`, `client.key` (Talos client certificate bundle). No Secret template exists. The sample CRs have `# TODO(user): Add fields here`. Neither is usable on real hardware.

The Talos client certs are generated by `talosctl gen config` and stored in `~/.talos/config`. Extract them with:
```bash
talosctl config info --output json | jq -r '.ca' | base64 -d > ca.crt
```
Or copy from `deploy/talos/generated/talosconfig` after `bootstrap-talos.sh` runs.

- [ ] **Step 1: Create the Secret template**

Create `deploy/kubernetes/samples/talos-client-secret.yaml`:

```yaml
# Talos client certificate Secret for TalosMachineConfig / TalosUpgrade controllers.
#
# Generate from your talosconfig after running bootstrap-talos.sh:
#
#   TALOSCONFIG=deploy/talos/generated/talosconfig
#   kubectl create secret generic talos-client-certs \
#     --namespace frame-system \
#     --from-file=ca.crt=<(talosctl --talosconfig $TALOSCONFIG config info --output json | jq -r '.ca' | base64 -d) \
#     --from-file=client.crt=<(talosctl --talosconfig $TALOSCONFIG config info --output json | jq -r '.crt' | base64 -d) \
#     --from-file=client.key=<(talosctl --talosconfig $TALOSCONFIG config info --output json | jq -r '.key' | base64 -d) \
#     --dry-run=client -o yaml > deploy/kubernetes/samples/talos-client-secret-filled.yaml
#   kubectl apply -f deploy/kubernetes/samples/talos-client-secret-filled.yaml
#
# DO NOT commit the filled secret. Add *-filled.yaml to .gitignore.
apiVersion: v1
kind: Secret
metadata:
  name: talos-client-certs
  namespace: frame-system
type: Opaque
data:
  # Base64-encoded values — fill in before applying
  ca.crt: REPLACE_BASE64_CA_CRT
  client.crt: REPLACE_BASE64_CLIENT_CRT
  client.key: REPLACE_BASE64_CLIENT_KEY
```

- [ ] **Step 2: Add generated secret to .gitignore**

Edit `.gitignore` (or create it). Add:
```
deploy/kubernetes/samples/*-filled.yaml
deploy/talos/generated/
```

- [ ] **Step 3: Fill in TalosMachineConfig sample**

Edit `config/samples/frame_v1alpha1_talosmachineconfig.yaml`:

```yaml
apiVersion: frame.plume-labs.io/v1alpha1
kind: TalosMachineConfig
metadata:
  name: worker-01-patch
  namespace: frame-system
spec:
  nodeName: worker-01           # must match the K8s Node name
  talosEndpoint: "192.168.10.10:50000"   # Talos API endpoint for this node
  talosSecretRef:
    name: talos-client-certs
    namespace: frame-system
  configPatch: |
    machine:
      sysctls:
        vm.max_map_count: "524288"
      kubelet:
        extraArgs:
          max-pods: "250"
```

- [ ] **Step 4: Fill in TalosUpgrade sample**

Edit `config/samples/frame_v1alpha1_talosupgrade.yaml`. Read the file first, then fill real fields:

```yaml
apiVersion: frame.plume-labs.io/v1alpha1
kind: TalosUpgrade
metadata:
  name: worker-01-upgrade
  namespace: frame-system
spec:
  nodeName: worker-01
  talosEndpoint: "192.168.10.10:50000"
  talosSecretRef:
    name: talos-client-certs
    namespace: frame-system
  image: ghcr.io/siderolabs/talos:v1.9.0    # target Talos version
```

- [ ] **Step 5: Verify samples are valid YAML**

```bash
python3 -c "import yaml; list(yaml.safe_load_all(open('config/samples/frame_v1alpha1_talosmachineconfig.yaml')))" && echo "OK"
python3 -c "import yaml; list(yaml.safe_load_all(open('config/samples/frame_v1alpha1_talosupgrade.yaml')))" && echo "OK"
```
Expected: `OK` for both.

- [ ] **Step 6: Commit**

```bash
git add deploy/kubernetes/samples/talos-client-secret.yaml \
        config/samples/frame_v1alpha1_talosmachineconfig.yaml \
        config/samples/frame_v1alpha1_talosupgrade.yaml \
        .gitignore
git commit -m "feat: add Talos client Secret template, complete TalosMachineConfig and TalosUpgrade samples"
```

---

## Task 6: GitOps repo parameterization (Flux + ArgoCD)

**Files:**
- Modify: `deploy/gitops/flux/sources/git-repository.yaml`
- Modify: `deploy/gitops/argocd/applications/cluster-control.yaml`
- Modify: `deploy/gitops/argocd/projects/cluster-infrastructure.yaml`
- Modify: `deploy/gitops/bootstrap-flux.sh` (verify env vars already used)

**Context:** Flux and ArgoCD manifests hardcode `https://github.com/your-org/cluster-gitops`. The `bootstrap-flux.sh` script already uses `$GITHUB_OWNER`/`$GITHUB_REPOSITORY` env vars, but the static YAML files still have placeholders. These should use a consistent placeholder comment so users know exactly what to replace.

- [ ] **Step 1: Parameterize Flux GitRepository**

Edit `deploy/gitops/flux/sources/git-repository.yaml`. Replace `https://github.com/your-org/cluster-gitops` with:

```yaml
# Replace GITHUB_OWNER and GITHUB_REPOSITORY with your GitOps repo before applying.
# Or use: flux bootstrap github --owner=<owner> --repository=<repo> (auto-generates this file)
  url: https://github.com/REPLACE_GITHUB_OWNER/REPLACE_GITHUB_REPOSITORY
```

- [ ] **Step 2: Parameterize ArgoCD Application**

Edit `deploy/gitops/argocd/applications/cluster-control.yaml`. Replace `https://github.com/your-org/cluster-gitops` with `https://github.com/REPLACE_GITHUB_OWNER/REPLACE_GITHUB_REPOSITORY`.

- [ ] **Step 3: Parameterize ArgoCD Project**

Edit `deploy/gitops/argocd/projects/cluster-infrastructure.yaml`. Same replacement.

- [ ] **Step 4: Verify bootstrap script already uses env vars**

```bash
grep "GITHUB_OWNER\|GITHUB_REPOSITORY" deploy/gitops/bootstrap-flux.sh
```
Expected: at least two matches showing both vars are used. If not found, add:
```bash
GITHUB_OWNER="${GITHUB_OWNER:?Set GITHUB_OWNER}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY}"
```
near the top of the script.

- [ ] **Step 5: Commit**

```bash
git add deploy/gitops/
git commit -m "fix: replace hardcoded GitOps repo URLs with REPLACE_ placeholders in Flux and ArgoCD manifests"
```

---

## End-to-end deploy checklist

After all tasks are complete, a full hardware deploy follows this sequence:

```bash
# 1. Set your values
export GITHUB_OWNER=myorg
export GITHUB_REPOSITORY=frame-gitops
export IMG=ghcr.io/myorg/frame-operator:v0.1.0
export IMG_UI=ghcr.io/myorg/frame-ui:v0.1.0

# 2. Bootstrap Talos cluster (if starting from bare metal)
./deploy/scripts/bootstrap-talos.sh <controlplane-ip> <worker-ips>

# 3. Install cluster pre-reqs
./deploy/scripts/bootstrap-prereqs.sh

# 4. Build and push images
make docker-build docker-push IMG=$IMG
make docker-build-ui docker-push-ui IMG_UI=$IMG_UI

# 5. Set image in kustomize overlays
make set-image-ui IMG_UI=$IMG_UI             # dev overlay
make set-image-ui-prod IMG_UI=$IMG_UI        # prod overlay
cd config/manager && kustomize edit set image controller=$IMG && cd -

# 6. Create ClusterIssuer (edit email first)
kubectl apply -f deploy/certmanager/cluster-issuer.yaml

# 7. Create Talos client Secret (fill certs first)
kubectl apply -f deploy/kubernetes/samples/talos-client-secret-filled.yaml

# 8. Deploy operator
kubectl apply -k config/default

# 9. Deploy UI
kustomize build deploy/kubernetes/overlays/production | kubectl apply -f -

# 10. Verify
kubectl -n frame-system get pods
kubectl -n cluster-control get pods
kubectl get framejobs -A
```
