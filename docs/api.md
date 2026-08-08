# API & SDK

Frame exposes the Kubernetes CRD API directly — there is no intermediate REST server. All reads and writes go through the standard Kubernetes API at `/apis/frame.plume-labs.io/v1alpha1/`.

---

## Authentication

### Development (kubectl proxy)

```bash
kubectl proxy --port=8001
```

Vite proxies `/apis/*` → `localhost:8001`. No token needed — kubectl uses your local kubeconfig.

### Production (ServiceAccount token)

Inject the token before mounting the app:

```html
<script>
  window.__FRAME_TOKEN__ = "eyJhbGc..."  <!-- SA token from K8s Secret -->
</script>
```

The SDK reads `window.__FRAME_TOKEN__` and adds `Authorization: Bearer <token>` to every request. The token must have RBAC rights on the Frame CRDs in the target namespace.

---

## TypeScript SDK

`src/lib/frame-sdk.ts` — `FrameClient` is the main entry point. Import it directly from the repo:

```typescript
import { FrameClient } from '@/lib/frame-sdk'

const frame = new FrameClient()
```

### Jobs

```typescript
// List all FrameJobs (all namespaces)
const { items } = await frame.jobs.list()

// Submit a job — creates a FrameJob CR
const job = await frame.jobs.submit({
  name:         'llm-finetune-v4',
  pipeline:     'training',
  serviceClass: 'HIGH',          // HIGH | MEDIUM | LOW
  priority:     'critical',      // critical | high | medium | low
  namespace:    'neura-prod',
  gpuCount:     8,
  parameters:   { epochs: '10', lr: '0.0001' },
})

// Cancel a job — deletes the FrameJob CR (and the Argo Workflow via finalizer)
await frame.jobs.cancel('llm-finetune-v4', 'neura-prod')

// Suspend / resume
await frame.jobs.suspend('llm-finetune-v4', 'neura-prod')
await frame.jobs.resume('llm-finetune-v4', 'neura-prod')
```

### Nodes

```typescript
// List FrameNodes
const { items } = await frame.nodes.list()

// Discover a new bare-metal node (creates a FrameNode CR in Discovering phase)
const { crName } = await frame.nodes.discover('192.168.10.25')

// Poll until Discovered, then configure
await frame.nodes.patchSpec(crName, {
  ip:           '192.168.10.25',
  role:         'worker',
  disk:         '/dev/nvme0n1',
  rack:         'rack-01',
  zone:         'zone-a',
  serviceClass: 'HIGH',
})

// Get node status
const status = await frame.nodes.getStatus(crName)
// → { phase, discoveredHostname, discoveredDisks, discoveredNICs, … }
```

### Scheduling policies

```typescript
// List SchedulingPolicy CRs
const { items } = await frame.scheduler.listPolicies()

// Create / update a policy
await frame.scheduler.applyPolicy({
  name:       'hpc-critical',
  scheduler:  'volcano',    // volcano | yunikorn | default
  queue:      'hpc',
  priority:   100,
  preemption: true,
  maxGPUs:    64,
  maxCPUs:    512,
})

// Delete
await frame.scheduler.deletePolicy('hpc-critical')
```

### Resource quotas

```typescript
// List FrameResourceQuota CRs
const { items } = await frame.resources.listQuotas()

// Set a namespace quota
await frame.resources.setQuota('neura-prod', {
  maxCPU:    '128',
  maxMemory: '512Gi',
  maxGPUs:   16,
})
```

### Health check

```typescript
const health = await frame.health()
// → { status: 'ok' | 'degraded', version: 'v1alpha1', uptime: 0 }
```

---

## Raw Kubernetes API

The Frame CRDs are standard Kubernetes resources. You can use `kubectl` directly:

```bash
# List all FrameJobs in a namespace
kubectl get framejobs -n neura-prod

# Inspect a job (shows conditions + events)
kubectl describe framejob llm-finetune-v4 -n neura-prod

# Create from a manifest
kubectl apply -f config/samples/frame_v1alpha1_framejob.yaml

# Watch status
kubectl get framejobs -n neura-prod -w

# Delete (triggers finalizer → removes Argo Workflow)
kubectl delete framejob llm-finetune-v4 -n neura-prod
```

---

## CRD API endpoints

All resources are namespaced under `frame.plume-labs.io/v1alpha1`.

| Resource | Plural | Shortname |
|---|---|---|
| FrameJob | `framejobs` | `fj` |
| FrameNode | `framenodes` | `fn` |
| SchedulingPolicy | `schedulingpolicies` | `sp` |
| FrameResourceQuota | `frameresourcequotas` | `frq` |
| TalosMachineConfig | `talosmachineconfigs` | `tmc` |
| TalosUpgrade | `talosupgrades` | `tu` |

Direct API path pattern:

```
/apis/frame.plume-labs.io/v1alpha1/namespaces/<namespace>/<plural>/<name>
```

Example curl (with kubectl proxy running):

```bash
curl http://localhost:8001/apis/frame.plume-labs.io/v1alpha1/namespaces/neura-prod/framejobs
```

---

## RBAC

Three tiers are defined in `config/rbac/`:

| Role | Can do |
|---|---|
| `frame-viewer` | `get`, `list`, `watch` on all Frame resources |
| `frame-editor` | viewer + `create`, `update`, `patch`, `delete` on FrameJob, SchedulingPolicy, FrameResourceQuota |
| `frame-admin` | editor + full access including FrameNode, TalosMachineConfig, TalosUpgrade |

Bind the appropriate role to the ServiceAccount whose token you inject via `window.__FRAME_TOKEN__`.

---

## Field reference

See [crd-reference.md](crd-reference.md) for the full field-level documentation of all seven CRDs.
