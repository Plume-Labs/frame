import { describe, it, expect, vi, afterEach } from 'vitest'

import { DEFAULT_CONFIG, config, loadConfig, type Integration } from './frame-config'
import { createFrameClient, FrameAPIError, type TaskRecord } from './frame-sdk'
import { MAX_ACTION_LENGTH } from './manifest-diff'
import {
  INFERENCE_SERVICES,
  actionsFor,
  applyWarning,
  canConfirm,
  confirmationFor,
  foreignManager,
  lastTaskFor,
  parseSelector,
  permissionFromError,
  reasonMaxLength,
  replicasFor,
  sanitizeReason,
  templateDrift,
  resolveDeployment,
  serviceState,
  type DeploymentLike,
  type Permissions,
  type PodLike,
} from './inference-services'

// ── Fixtures: shapes read off a live cluster, names and models made generic ──

const NS = 'inference'

function deploy(name: string, matchLabels: Record<string, string>, extra: Partial<DeploymentLike['metadata']> = {}): DeploymentLike {
  return {
    metadata: { name, namespace: NS, labels: matchLabels, ...extra },
    spec: { replicas: 1, selector: { matchLabels } },
    status: { replicas: 1, readyReplicas: 1 },
  }
}

const chat = deploy('llamacpp', { app: 'llamacpp' }, {
  annotations: { 'kubectl.kubernetes.io/last-applied-configuration': JSON.stringify({ spec: { replicas: 1 } }) },
  managedFields: [
    { manager: 'kubectl-client-side-apply', operation: 'Update', fieldsV1: { 'f:spec': { 'f:replicas': {} } } },
    { manager: 'kube-controller-manager', operation: 'Update', subresource: 'status', fieldsV1: {} },
  ],
})
const vision = deploy('llamacpp-vision', { app: 'llamacpp-vision' })
const tei = deploy('tei', { app: 'tei' })
/** The endpoint picker: Helm, server-side apply, no owner. */
const epp = deploy('llamacpp-epp', { inferencepool: 'llamacpp-epp' }, {
  labels: { 'app.kubernetes.io/managed-by': 'Helm', 'app.kubernetes.io/name': 'llamacpp-epp' },
  annotations: { 'meta.helm.sh/release-name': 'llamacpp-epp' },
  managedFields: [{ manager: 'helm', operation: 'Apply', fieldsV1: { 'f:spec': { 'f:replicas': {} } } }],
})
/** The gateway: owned by its Gateway, written by the controller's server-side apply. */
const gateway = deploy('inference-gateway', { 'app.kubernetes.io/name': 'inference-gateway' }, {
  labels: { 'gateway.networking.k8s.io/gateway-name': 'inference-gateway' },
  ownerReferences: [{ kind: 'Gateway', name: 'inference-gateway', controller: true }],
  managedFields: [{ manager: 'gateway-controller', operation: 'Apply', fieldsV1: {} }],
})
const ALL = [chat, vision, tei, epp, gateway]

const integ = (selector: string, deployment?: string): Integration => ({ namespace: NS, selector, port: 8080, deployment })

function pod(name: string, o: { phase?: string; ready?: boolean; start?: string; readyAt?: string; deleting?: boolean } = {}): PodLike {
  return {
    metadata: { name, deletionTimestamp: o.deleting ? '2026-01-01T00:00:00Z' : undefined },
    spec: { nodeName: 'node-a' },
    status: {
      phase: o.phase ?? 'Running',
      startTime: o.start,
      conditions: [{ type: 'Ready', status: o.ready ? 'True' : 'False', lastTransitionTime: o.readyAt }],
    },
  }
}

const allowed: Permissions = { patch: { state: 'allowed' }, scale: { state: 'allowed' } }
const chatDef = INFERENCE_SERVICES.find((s) => s.key === 'llamacpp')!
const teiDef = INFERENCE_SERVICES.find((s) => s.key === 'tei')!

// ── Selector → Deployment ────────────────────────────────────────────────────

describe('parseSelector', () => {
  it('reads plain equality terms', () => {
    expect(parseSelector('app=llamacpp')).toEqual({ app: 'llamacpp' })
    expect(parseSelector('a=b, c==d')).toEqual({ a: 'b', c: 'd' })
  })

  it('refuses what matchLabels cannot express', () => {
    for (const s of ['', 'app!=x', 'app in (a,b)', 'app', 'a=b,a=c']) expect(parseSelector(s)).toBeNull()
  })
})

describe('resolveDeployment', () => {
  it('maps each configured selector to the one Deployment whose matchLabels equal it', () => {
    const got = INFERENCE_SERVICES.map((s) => {
      const r = resolveDeployment(DEFAULT_CONFIG.integrations[s.key], ALL)
      return r.ok ? r.deployment.metadata.name : r.reason
    })
    expect(got).toEqual(['llamacpp', 'llamacpp-vision', 'tei'])
  })

  // Equal, not "contains": app=llamacpp must never land on the vision server.
  it('does not match a Deployment whose selector only shares a prefix or a subset', () => {
    const wider = deploy('wider', { app: 'llamacpp', tier: 'x' })
    const r = resolveDeployment(integ('app=llamacpp'), [wider, vision])
    expect(r.ok).toBe(false)
    // And the other way round: a narrower Deployment is not a match either.
    expect(resolveDeployment(integ('app=llamacpp,tier=x'), [chat]).ok).toBe(false)
  })

  it('offers nothing when two Deployments share the selector, and says to pick one', () => {
    const twin = deploy('llamacpp-b', { app: 'llamacpp' })
    const r = resolveDeployment(integ('app=llamacpp'), [chat, twin])
    expect(r).toEqual({ ok: false, reason: expect.stringContaining('2 Deployments') })
  })

  it('ignores a Deployment carrying matchExpressions', () => {
    const d = deploy('llamacpp', { app: 'llamacpp' })
    d.spec!.selector!.matchExpressions = [{ key: 'x', operator: 'Exists' }]
    expect(resolveDeployment(integ('app=llamacpp'), [d]).ok).toBe(false)
  })

  it('takes an explicit Deployment name over the selector', () => {
    const r = resolveDeployment(integ('app=nothing', 'tei'), ALL)
    expect(r.ok && r.deployment.metadata.name).toBe('tei')
    expect(resolveDeployment(integ('app=tei', 'missing'), ALL).ok).toBe(false)
  })
})

describe('gateway and EPP are never operable', () => {
  // Even a configuration pointed straight at them resolves to no button.
  it('refuses both, by selector and by explicit name', () => {
    for (const r of [
      resolveDeployment(integ('inferencepool=llamacpp-epp'), ALL),
      resolveDeployment(integ('app.kubernetes.io/name=inference-gateway'), ALL),
      resolveDeployment(integ('x=y', 'llamacpp-epp'), ALL),
      resolveDeployment(integ('x=y', 'inference-gateway'), ALL),
    ]) {
      expect(r.ok).toBe(false)
    }
  })

  // One guard per shape, each alone, so removing any one of them fails here.
  it.each([
    ['a controller owner', { ownerReferences: [{ kind: 'Gateway', name: 'g', controller: true }] }, 'controlled by Gateway g'],
    ['the gateway label', { labels: { 'gateway.networking.k8s.io/gateway-name': 'g' } }, 'Gateway API'],
    ['the Helm label', { labels: { 'app.kubernetes.io/managed-by': 'Helm' } }, 'Helm'],
    ['the Helm annotation', { annotations: { 'meta.helm.sh/release-name': 'r' } }, 'Helm'],
    ['a server-side apply manager', { managedFields: [{ manager: 'someone', operation: 'Apply' }] }, 'server-side apply (someone)'],
  ])('refuses on %s', (_, meta, reason) => {
    expect(foreignManager(deploy('x', { app: 'x' }, meta as Partial<DeploymentLike['metadata']>))).toContain(reason)
  })

  it('accepts a Deployment written by kubectl apply and kubectl patch', () => {
    expect(foreignManager(chat)).toBeUndefined()
    // A status Apply is the controller's, not a spec owner.
    expect(foreignManager(deploy('x', { app: 'x' }, { managedFields: [{ manager: 'k', operation: 'Apply', subresource: 'status' }] }))).toBeUndefined()
  })

  it('lists no gateway or EPP among the operable services', () => {
    expect(INFERENCE_SERVICES.map((s) => s.key)).toEqual(['llamacpp', 'llamacppVision', 'tei'])
  })
})

// ── State ────────────────────────────────────────────────────────────────────

describe('serviceState', () => {
  it('tells a loading pod from a ready one, and measures the load', () => {
    const loading = serviceState(chat, [pod('p1', { ready: false })])
    expect(loading.pod).toBe('loading')
    const ready = serviceState(chat, [pod('p1', { ready: true, start: '2026-01-01T10:00:00Z', readyAt: '2026-01-01T10:07:00Z' })])
    expect(ready.pod).toBe('ready')
    expect(ready.lastLoadSeconds).toBe(420)
    expect(ready.podName).toBe('p1')
  })

  it('prefers the serving pod over a terminating or finished one', () => {
    const s = serviceState(chat, [pod('old', { deleting: true }), pod('done', { phase: 'Succeeded' }), pod('new', { ready: true })])
    expect(s.podName).toBe('new')
  })

  it('reads a stopped service as stopped', () => {
    const stopped = { ...chat, spec: { ...chat.spec, replicas: 0 } }
    expect(serviceState(stopped, []).pod).toBe('stopped')
    expect(serviceState(stopped, []).desired).toBe(0)
  })

  it('reads the last restart from the template annotation', () => {
    const d = { ...tei, spec: { ...tei.spec, template: { metadata: { annotations: { 'kubectl.kubernetes.io/restartedAt': '2026-01-02T00:00:00Z' } } } } }
    expect(serviceState(d, []).lastRestart).toBe('2026-01-02T00:00:00Z')
    expect(serviceState(tei, []).lastRestart).toBeUndefined()
  })

  it('sees that kubectl-client-side-apply holds replicas', () => {
    expect(serviceState(chat, []).replicasHeldByApply).toBe(true)
    expect(serviceState(vision, []).replicasHeldByApply).toBe(false)
  })
})

describe('applyWarning', () => {
  it('always says a kubectl apply undoes a stop', () => {
    for (const d of [chat, vision]) expect(applyWarning(serviceState(d, []))).toContain('kubectl apply')
  })

  it('names the replica count the manifest will restore once stopped', () => {
    const stopped = { ...chat, spec: { ...chat.spec, replicas: 0 } }
    expect(applyWarning(serviceState(stopped, []))).toContain('replicas: 1')
  })
})

// ── Permissions and buttons ──────────────────────────────────────────────────

describe('actionsFor', () => {
  it('offers restart and stop on a running service, start on a stopped one', () => {
    expect(actionsFor({ desired: 1 }, allowed).map((a) => a.kind)).toEqual(['restart', 'stop'])
    expect(actionsFor({ desired: 0 }, allowed).map((a) => a.kind)).toEqual(['start'])
  })

  it('disables a button without the right, and says why', () => {
    const perms: Permissions = { patch: { state: 'denied', reason: 'no patch' }, scale: { state: 'allowed' } }
    const [restart, stop] = actionsFor({ desired: 1 }, perms)
    expect(restart).toEqual({ kind: 'restart', enabled: false, why: 'no patch' })
    expect(stop.enabled).toBe(true)
    const [start] = actionsFor({ desired: 0 }, { ...perms, scale: { state: 'denied', reason: 'no scale' } })
    expect(start).toEqual({ kind: 'start', enabled: false, why: 'no scale' })
  })

  it('keeps buttons disabled while the check runs, and enabled when the check itself failed', () => {
    expect(actionsFor({ desired: 1 }, { patch: { state: 'checking' }, scale: { state: 'checking' } }).every((a) => !a.enabled)).toBe(true)
    expect(actionsFor({ desired: 1 }, { patch: { state: 'unknown', reason: 'x' }, scale: { state: 'unknown', reason: 'x' } }).every((a) => a.enabled)).toBe(true)
  })
})

describe('permissionFromError', () => {
  it('reads a 403 as a denial and anything else as no answer', () => {
    expect(permissionFromError(new FrameAPIError(403, 'forbidden'), 'patch deployments', NS)).toEqual({
      state: 'denied',
      reason: expect.stringContaining('patch deployments in inference'),
    })
    expect(permissionFromError(new FrameAPIError(500, 'boom'), 'patch deployments', NS).state).toBe('unknown')
  })
})

describe('replicasFor', () => {
  it('stops at 0 and starts at 1', () => {
    expect(replicasFor(chatDef, 'stop')).toBe(0)
    expect(replicasFor(chatDef, 'start')).toBe(1)
    expect(replicasFor(teiDef, 'start')).toBe(1)
  })
})

// ── Confirmations ────────────────────────────────────────────────────────────

describe('confirmationFor', () => {
  const target = { namespace: NS, name: 'llamacpp' }

  it('makes a stop retype the name and give a reason', () => {
    const c = confirmationFor('stop', chatDef, target)
    expect(c.typeToConfirm).toBe('llamacpp')
    expect(c.requireReason).toBe(true)
    expect(canConfirm(c, 'llamacpp', '')).toBe(false)
    expect(canConfirm(c, 'llamacp', 'maintenance')).toBe(false)
    expect(canConfirm(c, ' llamacpp ', 'maintenance')).toBe(true)
  })

  it('asks nothing more than a click for restart and start', () => {
    for (const a of ['restart', 'start'] as const) {
      const c = confirmationFor(a, chatDef, target)
      expect(c.typeToConfirm).toBeUndefined()
      expect(canConfirm(c, '', '')).toBe(true)
    }
  })

  it('states the effect and the outage, with the measured load when there is one', () => {
    const typical = confirmationFor('restart', chatDef, target)
    expect(typical.effect).toContain('restartedAt')
    expect(typical.outage).toContain('chat LLM')
    expect(typical.outage).toContain('about 6 min (typical)')
    const measured = confirmationFor('restart', chatDef, target, { lastLoadSeconds: 432, inFlight: 2 })
    expect(measured.outage).toContain('about 7 min (last load measured)')
    expect(measured.effect).toContain('2 request(s) in flight')
    expect(confirmationFor('stop', chatDef, target).effect).toContain('0 replicas')
    expect(confirmationFor('stop', chatDef, target).outage).toContain('until someone starts it again')
    expect(confirmationFor('start', chatDef, target).effect).toContain('1 replica')
  })
})

describe('lastTaskFor', () => {
  it('finds the newest write to the Deployment or its scale subresource', () => {
    const t = (target: string, startedAt: string): TaskRecord => ({
      name: startedAt, user: 'u', verb: 'patch', action: 'a', target, phase: 'Succeeded', startedAt,
    })
    const tasks = [
      t('inference/deployments/llamacpp', '2026-01-01T00:00:00Z'),
      t('inference/deployments/llamacpp/scale', '2026-01-03T00:00:00Z'),
      t('inference/deployments/llamacpp-vision', '2026-01-04T00:00:00Z'),
    ]
    expect(lastTaskFor(tasks, NS, 'llamacpp')?.startedAt).toBe('2026-01-03T00:00:00Z')
  })
})

// ── Config ───────────────────────────────────────────────────────────────────

describe('frame-config', () => {
  afterEach(() => vi.unstubAllGlobals())

  function stubConfigMap(stored: unknown) {
    vi.stubGlobal('window', {})
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ data: { 'config.json': JSON.stringify(stored) } }), { status: 200 })))
  }

  it('declares the vision server beside chat and embeddings', () => {
    expect(DEFAULT_CONFIG.integrations.llamacppVision.selector).not.toBe(DEFAULT_CONFIG.integrations.llamacpp.selector)
  })

  it('keeps a valid Deployment name and drops one that could leave its path segment', async () => {
    stubConfigMap({ integrations: { llamacpp: { deployment: 'chat-server' }, tei: { deployment: '../../secrets' } } })
    await loadConfig()
    expect(config().integrations.llamacpp.deployment).toBe('chat-server')
    expect(config().integrations.tei.deployment).toBeUndefined()
    expect(config().integrations.tei.selector).toBe(DEFAULT_CONFIG.integrations.tei.selector)
  })
})

// ── What goes on the wire ────────────────────────────────────────────────────

interface Seen {
  url: string
  method: string
  headers: Record<string, string>
  body?: string
}

function capture(respond: (url: string) => Response = () => new Response('{}', { status: 200 })): Seen[] {
  vi.stubGlobal('window', globalThis)
  const seen: Seen[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      seen.push({
        url,
        method: init?.method ?? 'GET',
        headers: (init?.headers as Record<string, string>) ?? {},
        body: init?.body as string | undefined,
      })
      return respond(url)
    }),
  )
  return seen
}

const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })

describe('patches sent', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('restarts with exactly the restartedAt annotation', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-05T12:00:00.000Z'))
    const seen = capture()
    await createFrameClient().workloads.restart('Deployment', NS, 'llamacpp')
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/inference/deployments/llamacpp')
    expect(seen[0].method).toBe('PATCH')
    expect(seen[0].headers['Content-Type']).toBe('application/strategic-merge-patch+json')
    expect(seen[0].body).toBe(
      '{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-01-05T12:00:00.000Z"}}}}}',
    )
  })

  it('stops with replicas 0 on /scale, recording the reason', async () => {
    const seen = capture()
    await createFrameClient().workloads.scale('Deployment', NS, 'llamacpp', replicasFor(chatDef, 'stop'), 'disk swap')
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/inference/deployments/llamacpp/scale')
    expect(seen[0].headers['Content-Type']).toBe('application/merge-patch+json')
    expect(seen[0].body).toBe('{"spec":{"replicas":0}}')
    expect(seen[0].headers['X-Frame-Action']).toBe('scale deployment inference/llamacpp to 0: disk swap')
  })

  it('starts with replicas 1 on /scale', async () => {
    const seen = capture()
    await createFrameClient().workloads.scale('Deployment', NS, 'llamacpp', replicasFor(chatDef, 'start'))
    expect(seen[0].body).toBe('{"spec":{"replicas":1}}')
    expect(seen[0].headers['X-Frame-Action']).toBe('scale deployment inference/llamacpp to 1')
  })

  // A dry run is authorized like the write and stores nothing; the uiproxy
  // records no FrameTask for it. An empty patch changes nothing either way.
  it('probes permissions with dry-run empty patches on the same paths', async () => {
    const seen = capture()
    const c = createFrameClient()
    await c.workloads.probeWrite(NS, 'llamacpp', 'patch')
    await c.workloads.probeWrite(NS, 'llamacpp', 'scale')
    expect(seen.map((s) => [s.method, s.url, s.body])).toEqual([
      ['PATCH', '/apis/apps/v1/namespaces/inference/deployments/llamacpp?dryRun=All', '{}'],
      ['PATCH', '/apis/apps/v1/namespaces/inference/deployments/llamacpp/scale?dryRun=All', '{}'],
    ])
    expect(seen.every((s) => !s.headers['X-Frame-Action'])).toBe(true)
  })

  it('surfaces a denied probe as a 403', async () => {
    capture(() => new Response(JSON.stringify({ message: 'forbidden' }), { status: 403 }))
    const err = await createFrameClient().workloads.probeWrite(NS, 'llamacpp', 'scale').catch((e) => e)
    expect(permissionFromError(err, 'patch deployments/scale', NS).state).toBe('denied')
  })
})

describe('inference.list', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('resolves the three servers, reads the served model, and never the gateway or EPP', async () => {
    const seen = capture((url) => {
      if (url === '/apis/apps/v1/namespaces/inference/deployments') return json({ items: ALL })
      if (url.includes('/pods?')) return json({ items: [pod('p-1', { ready: true })] })
      if (url.endsWith('/props')) return json({ model_alias: 'example-model', total_slots: 2, default_generation_settings: { n_ctx: 4096 } })
      if (url.endsWith('/info')) return json({ model_id: 'example/embedder' })
      return json({})
    })
    // The config test above left a stored `deployment` in force; back to defaults.
    await loadConfig()
    const views = await createFrameClient().inference.list()
    expect(views.map((v) => [v.def.key, v.deployment, v.model])).toEqual([
      ['llamacpp', 'llamacpp', 'example-model'],
      ['llamacppVision', 'llamacpp-vision', 'example-model'],
      ['tei', 'tei', 'example/embedder'],
    ])
    expect(views[0].contextSize).toBe(4096)
    // Pods are listed by each Deployment's own selector.
    expect(seen.some((s) => s.url.includes(encodeURIComponent('app=llamacpp-vision')))).toBe(true)
    // Read-only: nothing but GETs.
    expect(seen.every((s) => s.method === 'GET')).toBe(true)
  })
})

// ── Reason: what is typed is what is recorded ────────────────────────────────

describe('reason', () => {
  afterEach(() => vi.unstubAllGlobals())
  const stop = confirmationFor('stop', chatDef, { namespace: NS, name: 'llamacpp' })

  it('folds accents instead of dropping the letter', () => {
    expect(sanitizeReason('arrêt  pour\tmaintenance')).toBe('arret pour maintenance')
  })

  it('refuses a reason that is nothing but emoji or symbols once filtered', () => {
    expect(canConfirm(stop, 'llamacpp', '🔥🛑')).toBe(false)
    expect(canConfirm(stop, 'llamacpp', '🔥 ok')).toBe(true)
  })

  it('bounds the reason so the whole label fits the FrameTask cap', () => {
    const max = reasonMaxLength(NS, 'llamacpp', MAX_ACTION_LENGTH)
    expect(`scale deployment inference/llamacpp to 0: ${'x'.repeat(max)}`.length).toBe(MAX_ACTION_LENGTH)
  })

  it('records the folded reason, whole, at the maximum length', async () => {
    const seen = capture()
    const max = reasonMaxLength(NS, 'llamacpp', MAX_ACTION_LENGTH)
    await createFrameClient().workloads.scale('Deployment', NS, 'llamacpp', 0, 'arrêt ' + 'x'.repeat(max - 6))
    const label = seen[0].headers['X-Frame-Action']
    expect(label.length).toBe(MAX_ACTION_LENGTH)
    expect(label.endsWith('arret ' + 'x'.repeat(max - 6))).toBe(true)
  })
})

// ── Drift against last-applied ───────────────────────────────────────────────

describe('templateDrift', () => {
  function withTemplate(live: object, applied: object): DeploymentLike {
    return {
      ...vision,
      metadata: {
        ...vision.metadata,
        annotations: {
          'kubectl.kubernetes.io/last-applied-configuration': JSON.stringify({ spec: { template: { spec: { containers: [applied] } } } }),
        },
      },
      spec: { ...vision.spec, template: { spec: { containers: [live as never] } } },
    }
  }

  it('names each arg, image and env var that a kubectl apply would revert', () => {
    const d = withTemplate(
      { name: 'server', image: 'img:2', args: ['-a', '1', '-b', '9'], env: [{ name: 'X', value: '1' }, { name: 'Y', value: 'y' }] },
      { name: 'server', image: 'img:1', args: ['-a', '2', '-b', '9'], env: [{ name: 'X', value: '2' }] },
    )
    expect(templateDrift(d)).toEqual([
      'containers[server].image',
      'containers[server].args[1]',
      'containers[server].env[X]',
      'containers[server].env[Y]',
    ])
    const w = applyWarning(serviceState(d, []))
    expect(w).toContain('containers[server].args[1]')
    expect(w).toContain('reverts those too')
  })

  it('finds nothing when live equals last-applied, or when there is no last-applied', () => {
    const c = { name: 'server', image: 'img:1', args: ['-a'], env: [{ name: 'X', value: '1' }] }
    expect(templateDrift(withTemplate(c, c))).toEqual([])
    expect(templateDrift(tei)).toEqual([])
    expect(applyWarning(serviceState(withTemplate(c, c), []))).not.toContain('differs')
  })
})
