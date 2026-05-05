/**
 * Frame REST API Server
 *
 * Exposes the Frame framework's operator API over HTTP so that workloads,
 * CI systems, and operator CLIs can interact with the platform programmatically
 * instead of only through the UI.
 *
 * Run:  npm run server
 * Docs: http://localhost:4000/api-docs  (or see deploy/api/openapi.yaml)
 */

import express, { Request, Response, NextFunction } from 'express'
import { randomUUID } from 'crypto'

const app = express()
app.use(express.json())

// ── CORS (restrict to known origins via env; defaults to UI dev server) ──────
const ALLOWED_ORIGINS = process.env.FRAME_ALLOWED_ORIGINS
  ? process.env.FRAME_ALLOWED_ORIGINS.split(',').map((o) => o.trim())
  : ['http://localhost:5173', 'http://localhost:4173']

app.use((req: Request, res: Response, next: NextFunction) => {
  const origin = req.headers.origin
  if (origin && ALLOWED_ORIGINS.includes(origin)) {
    res.setHeader('Access-Control-Allow-Origin', origin)
  }
  res.setHeader('Vary', 'Origin')
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, PUT, DELETE, OPTIONS')
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type, Authorization')
  if (req.method === 'OPTIONS') { res.sendStatus(204); return }
  next()
})

// ── In-memory state (replace with real cluster client in production) ─────────

type NodeStatus = 'online' | 'degraded' | 'offline' | 'provisioning'
type ServiceClass = 'HIGH' | 'MEDIUM' | 'LOW'
type JobStatus   = 'queued' | 'running' | 'completed' | 'failed'

interface NodeRecord {
  id: string; name: string; status: NodeStatus
  serviceClass: ServiceClass; zone: string; rackId: string
  cpu: number; memory: number; storage: number
  gpuCount: number; gpuModel: string
}

interface JobRecord {
  id: string; name: string; pipeline: string
  status: JobStatus; serviceClass: ServiceClass
  priority: 'critical' | 'high' | 'medium' | 'low'
  namespace: string; gpuCount: number
  createdAt: string; startedAt?: string; completedAt?: string
}

interface Policy {
  name: string; scheduler: 'volcano' | 'yunikorn' | 'default'
  queue: string; priority: number; preemption: boolean
  maxGPUs: number; maxCPUs: number
}

interface ResourceQuota {
  namespace: string
  maxCPU: string; maxMemory: string; maxGPUs: number
  usedCPU: string; usedMemory: string; usedGPUs: number
}

// Seed nodes
const nodes: NodeRecord[] = Array.from({ length: 8 }, (_, i) => ({
  id:           `node-${String(i + 1).padStart(3, '0')}`,
  name:         `frame-worker-${i + 1}`,
  status:       (['online', 'online', 'online', 'degraded'] as NodeStatus[])[i % 4],
  serviceClass: (['HIGH', 'HIGH', 'MEDIUM', 'LOW'] as ServiceClass[])[i % 4],
  zone:         `zone-${String.fromCharCode(65 + (i % 3))}`,
  rackId:       `rack-${(i % 4) + 1}`,
  cpu:          Math.round(40 + Math.random() * 50),
  memory:       Math.round(30 + Math.random() * 60),
  storage:      Math.round(20 + Math.random() * 70),
  gpuCount:     [0, 0, 2, 4, 8][i % 5],
  gpuModel:     ['None', 'A100-80GB', 'H100-SXM5'][i % 3],
}))

const jobs: JobRecord[] = [
  {
    id: randomUUID(), name: 'llm-finetune-v3', pipeline: 'training',
    status: 'running', serviceClass: 'HIGH', priority: 'critical',
    namespace: 'neura-prod', gpuCount: 8,
    createdAt: new Date(Date.now() - 3_600_000).toISOString(),
    startedAt:  new Date(Date.now() - 3_500_000).toISOString(),
  },
  {
    id: randomUUID(), name: 'embedding-inference-batch', pipeline: 'inference',
    status: 'queued', serviceClass: 'MEDIUM', priority: 'high',
    namespace: 'neura-prod', gpuCount: 4,
    createdAt: new Date(Date.now() -   300_000).toISOString(),
  },
]

const policies: Policy[] = [
  { name: 'hpc-critical', scheduler: 'volcano',  queue: 'hpc',    priority: 100, preemption: true,  maxGPUs: 64,  maxCPUs: 512 },
  { name: 'batch-low',    scheduler: 'yunikorn', queue: 'batch',  priority:  10, preemption: false, maxGPUs:  8,  maxCPUs: 128 },
  { name: 'inference',    scheduler: 'default',  queue: 'infer',  priority:  50, preemption: false, maxGPUs: 16,  maxCPUs: 256 },
]

const quotas: ResourceQuota[] = [
  { namespace: 'neura-prod', maxCPU: '512', maxMemory: '2048Gi', maxGPUs: 64, usedCPU: '312', usedMemory: '1280Gi', usedGPUs: 40 },
  { namespace: 'neura-dev',  maxCPU: '128', maxMemory:  '512Gi', maxGPUs:  8, usedCPU:  '24', usedMemory:  '96Gi',  usedGPUs:  2 },
]

// ── Health ───────────────────────────────────────────────────────────────────

app.get('/api/health', (_req: Request, res: Response) => {
  res.json({ status: 'ok', version: '1.0.0', uptime: process.uptime() })
})

// ── Nodes ────────────────────────────────────────────────────────────────────

app.get('/api/nodes', (_req: Request, res: Response) => {
  res.json({ items: nodes, total: nodes.length })
})

app.get('/api/nodes/:id', (req: Request, res: Response) => {
  const node = nodes.find((n) => n.id === req.params.id)
  if (!node) { res.status(404).json({ error: 'Node not found' }); return }
  res.json(node)
})

// ── Jobs ─────────────────────────────────────────────────────────────────────

app.get('/api/jobs', (_req: Request, res: Response) => {
  res.json({ items: jobs, total: jobs.length })
})

interface JobSubmitBody {
  name?: string; pipeline?: string; serviceClass?: ServiceClass
  priority?: 'critical' | 'high' | 'medium' | 'low'
  namespace?: string; gpuCount?: number
}

app.post('/api/jobs', (req: Request<object, object, JobSubmitBody>, res: Response) => {
  const { name, pipeline, serviceClass = 'MEDIUM', priority = 'medium', namespace = 'default', gpuCount = 0 } = req.body
  if (!name || !pipeline) {
    res.status(400).json({ error: "'name' and 'pipeline' are required" })
    return
  }
  const job: JobRecord = {
    id: randomUUID(), name, pipeline, status: 'queued',
    serviceClass, priority, namespace, gpuCount,
    createdAt: new Date().toISOString(),
  }
  jobs.push(job)
  res.status(201).json(job)
})

app.delete('/api/jobs/:id', (req: Request, res: Response) => {
  const idx = jobs.findIndex((j) => j.id === req.params.id)
  if (idx === -1) { res.status(404).json({ error: 'Job not found' }); return }
  const [job] = jobs.splice(idx, 1)
  res.json({ cancelled: true, job })
})

// ── Scheduler policies ───────────────────────────────────────────────────────

app.get('/api/scheduler/policies', (_req: Request, res: Response) => {
  res.json({ items: policies, total: policies.length })
})

interface PolicyBody extends Partial<Policy> { name?: string }

app.post('/api/scheduler/policies', (req: Request<object, object, PolicyBody>, res: Response) => {
  const body = req.body as Policy
  if (!body.name) { res.status(400).json({ error: '`name` is required' }); return }
  const existing = policies.findIndex((p) => p.name === body.name)
  if (existing >= 0) {
    policies[existing] = { ...policies[existing], ...body }
    res.json(policies[existing])
  } else {
    const policy: Policy = { scheduler: 'default', queue: 'default', priority: 50, preemption: false, maxGPUs: 8, maxCPUs: 64, ...body }
    policies.push(policy)
    res.status(201).json(policy)
  }
})

app.delete('/api/scheduler/policies/:name', (req: Request, res: Response) => {
  const idx = policies.findIndex((p) => p.name === req.params.name)
  if (idx === -1) { res.status(404).json({ error: 'Policy not found' }); return }
  const [policy] = policies.splice(idx, 1)
  res.json({ deleted: true, policy })
})

// ── Resource quotas ──────────────────────────────────────────────────────────

app.get('/api/resources/quotas', (_req: Request, res: Response) => {
  res.json({ items: quotas, total: quotas.length })
})

interface QuotaBody extends Partial<ResourceQuota> { namespace?: string }

app.put('/api/resources/quotas/:namespace', (req: Request, res: Response) => {
  const idx = quotas.findIndex((q) => q.namespace === req.params.namespace)
  const body = req.body as QuotaBody
  if (idx === -1) {
    const quota: ResourceQuota = {
      namespace: req.params.namespace,
      maxCPU: '64', maxMemory: '256Gi', maxGPUs: 4,
      usedCPU: '0', usedMemory: '0Gi', usedGPUs: 0,
      ...body,
    }
    quotas.push(quota)
    res.status(201).json(quota)
  } else {
    quotas[idx] = { ...quotas[idx], ...body, namespace: req.params.namespace }
    res.json(quotas[idx])
  }
})

// ── Service classes ──────────────────────────────────────────────────────────

app.get('/api/resources/service-classes', (_req: Request, res: Response) => {
  const summary = (['HIGH', 'MEDIUM', 'LOW'] as ServiceClass[]).map((sc) => ({
    serviceClass: sc,
    nodeCount: nodes.filter((n) => n.serviceClass === sc).length,
    totalGPUs: nodes.filter((n) => n.serviceClass === sc).reduce((s, n) => s + n.gpuCount, 0),
  }))
  res.json({ items: summary })
})

// ── 404 catch-all ────────────────────────────────────────────────────────────

app.use((_req: Request, res: Response) => {
  res.status(404).json({ error: 'Not found', hint: 'See deploy/api/openapi.yaml for available endpoints' })
})

// ── Start ─────────────────────────────────────────────────────────────────────

const PORT = Number(process.env.FRAME_API_PORT ?? 4000)
app.listen(PORT, () => {
  console.log(`Frame API server listening on http://localhost:${PORT}`)
  console.log(`OpenAPI spec: deploy/api/openapi.yaml`)
})

export default app
