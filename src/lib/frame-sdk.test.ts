/// <reference types="vite/client" />
import { describe, it, expect, vi, afterEach } from 'vitest'
import { __testing, createFrameClient, FrameAPIError, projectToFull, type MetricSeries } from './frame-sdk'
import { __resetForTests as resetAuthForTests, currentSession } from './auth'
// Raw source, for the structural guard at the bottom of this file.
import frameSdkSource from './frame-sdk.ts?raw'

/** `n` samples one hour apart, starting at `from` and moving `perDay` %/day. */
function series(from: number, perDay: number, n = 12): MetricSeries {
  const hour = 3600_000
  return {
    metric: 'CPU',
    current: 0,
    history: Array.from({ length: n }, (_, i) => ({
      t: 1_700_000_000_000 + i * hour,
      v: from + (perDay / 24) * i,
    })),
  }
}

describe('projectToFull', () => {
  it('projects the days left from the slope', () => {
    // Starts at 50% and climbs 10%/day over 11 hours, so the *last* sample —
    // the one headroom is measured from — is 54.58%, leaving 45.42 points.
    const { projectedFullDays } = projectToFull(series(50, 10))
    expect(projectedFullDays).toBeCloseTo(45.4166 / 10, 2)
  })

  it('reports no projection for a flat series', () => {
    expect(projectToFull(series(50, 0)).projectedFullDays).toBeNull()
  })

  it('reports no projection when usage is falling', () => {
    expect(projectToFull(series(50, -10)).projectedFullDays).toBeNull()
  })

  it('treats imperceptible drift as flat rather than a far-off date', () => {
    // 0.05 %/day would otherwise project ~1000 days and read as a real finding.
    expect(projectToFull(series(50, 0.05)).projectedFullDays).toBeNull()
  })

  it('takes current from the last sample, not the first', () => {
    expect(projectToFull(series(50, 10)).current).toBeCloseTo(50 + (10 / 24) * 11, 5)
  })

  it('cannot fit a line through fewer than two points', () => {
    expect(projectToFull(series(50, 10, 1)).projectedFullDays).toBeNull()
    expect(projectToFull(series(50, 10, 0)).projectedFullDays).toBeNull()
  })
})

// crToQuota, crToNode and crToPolicy are module-private, exposed only via the
// __testing barrel at the bottom of frame-sdk.ts for these tests.
describe('crToQuota', () => {
  it('reads the real usage the controller aggregates', () => {
    const q = __testing.crToQuota({
      metadata: { name: 'quota-high', namespace: 'default' },
      spec: { serviceClass: 'HIGH', maxGPUs: 4, maxCPU: '8', maxMemory: '16Gi' },
      status: {
        used: { 'limits.cpu': '3', 'limits.memory': '6Gi', 'requests.nvidia.com/gpu': '2' },
        namespaces: 2,
      },
    })
    expect(q.usedCPU).toBe('3')
    expect(q.usedMemory).toBe('6Gi')
    expect(q.usedGPUs).toBe(2)
    expect(q.namespaces).toBe(2)
  })

  it('falls back when no namespace has reported usage', () => {
    const q = __testing.crToQuota({
      metadata: { name: 'quota-low', namespace: 'default' },
      spec: { serviceClass: 'LOW' },
    })
    expect(q.usedCPU).toBe('0')
    expect(q.usedGPUs).toBe(0)
    expect(q.namespaces).toBe(0)
  })
})

describe('crToNode', () => {
  it('takes the GPU count from status.allocatable, not a spec field that does not exist', () => {
    const n = __testing.crToNode({
      metadata: { name: 'w2' },
      spec: { ip: '10.0.0.2', serviceClass: 'HIGH' },
      status: { allocatable: { cpu: '8', memory: '32Gi', 'nvidia.com/gpu': '1' } },
    })
    expect(n.gpuCount).toBe(1)
  })

  it('reports zero GPUs when allocatable carries no GPU key', () => {
    const n = __testing.crToNode({
      metadata: { name: 'w3' },
      spec: { ip: '10.0.0.3', serviceClass: 'LOW' },
      status: { allocatable: { cpu: '4', memory: '16Gi' } },
    })
    expect(n.gpuCount).toBe(0)
  })
})

describe('crToPolicy', () => {
  it('no longer reports resource ceilings SchedulingPolicySpec never had', () => {
    const p = __testing.crToPolicy({
      metadata: { name: 'neura-default' },
      spec: { scheduler: 'volcano', queueName: 'neura-high', queueWeight: 100 },
    }) as unknown as Record<string, unknown>
    expect(p).not.toHaveProperty('maxGPUs')
    expect(p).not.toHaveProperty('maxCPUs')
  })
})

// ── Phase mapping after F2 ───────────────────────────────────────────────────
//
// v1beta1 removed status.phase from FrameJob and FrameNode. Everything below
// asserts that the mappers now read the Ready condition's reason instead, and
// — more importantly — that they answer sensibly for the object shapes that
// actually exist on the cluster rather than only for the tidy ones.

describe('mapJobPhase', () => {
  it('reads the lifecycle off the Ready condition, not a stored phase', () => {
    const running = __testing.crToJob({
      metadata: { name: 'j1', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
      status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Running' }] },
    })
    expect(running.status).toBe('running')

    const done = __testing.crToJob({
      metadata: { name: 'j2', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
      status: { conditions: [{ type: 'Ready', status: 'True', reason: 'Completed' }] },
    })
    expect(done.status).toBe('completed')

    const failed = __testing.crToJob({
      metadata: { name: 'j3', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
      status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Failed' }] },
    })
    expect(failed.status).toBe('failed')
  })

  it('ignores a non-Ready condition that happens to carry a phase-shaped reason', () => {
    // A Degraded/Running condition must not be mistaken for the lifecycle:
    // find() over the whole list would match it if the type check were dropped.
    const j = __testing.crToJob({
      metadata: { name: 'j4', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
      status: {
        conditions: [
          { type: 'Degraded', status: 'True', reason: 'Running' },
          { type: 'Ready', status: 'True', reason: 'Completed' },
        ],
      },
    })
    expect(j.status).toBe('completed')
  })

  it('does not read completionTime, so a pre-invariant job never reads healthy', () => {
    // This is `neura-embed-refresh` as it is stored on the test cluster today,
    // minus the status.phase v1beta1 no longer serves: generation 1, a
    // write-once Submitted/WorkflowCreated condition from the pre-invariant
    // build, no Ready condition, and both timestamps set.
    //
    // It reads `queued`, which is a real regression from the `completed` the
    // removed field showed — and the correct one. The controller writes
    // completionTime on Failed as well as on Completed, so inferring
    // `completed` from it would render an old failure as healthy. The outcome
    // of this object is genuinely not recorded anywhere in it; a re-reconcile
    // is what restores it. The server-side v1alpha1 projection makes the same
    // call, answering `Submitted`, which this narrower domain type spells
    // `queued`.
    const legacy = __testing.crToJob({
      metadata: { name: 'neura-embed-refresh', namespace: 'default', creationTimestamp: '2026-07-28T18:22:09Z' },
      spec: { pipeline: 'neura-inference-dag', serviceClass: 'HIGH', priority: 'high', gpuCount: 0, suspended: false },
      status: {
        argoWorkflowName: 'neura-embed-refresh',
        observedGeneration: 1,
        startTime: '2026-07-28T18:22:09Z',
        completionTime: '2026-07-28T18:23:33Z',
        conditions: [{
          type: 'Submitted', status: 'True', reason: 'WorkflowCreated',
          message: 'ArgoWorkflow default/neura-embed-refresh created',
          observedGeneration: 1, lastTransitionTime: '2026-07-28T18:22:09Z',
        }],
      },
    })
    expect(legacy.status).toBe('queued')
    expect(legacy.completedAt).toBe('2026-07-28T18:23:33Z')
  })

  it('takes the namespace from metadata, since spec.namespace is gone', () => {
    const j = __testing.crToJob({
      metadata: { name: 'j5', namespace: 'team-a' },
      spec: { pipeline: 'neura-training-dag' },
    })
    expect(j.namespace).toBe('team-a')
  })

  it('agrees with the CRD default when serviceClass is unset', () => {
    // The schema defaults it to LOW. Answering MEDIUM here was half of the
    // kubectl-versus-UI disagreement the freeze removed.
    const j = __testing.crToJob({
      metadata: { name: 'j6', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
    })
    expect(j.serviceClass).toBe('LOW')
    expect(j.priority).toBe('medium')
  })
})

describe('mapNodePhase', () => {
  it('reads a discovered node off the Ready condition', () => {
    // All three FrameNodes on the test cluster carry exactly this condition.
    const discovered = __testing.crToNode({
      metadata: { name: 'neura-k3s-w1', namespace: 'default' },
      spec: { ip: '192.168.2.202', serviceClass: 'MEDIUM', zone: 'local', rack: 'rack-01', hostname: 'neura-k3s-w1' },
      status: {
        observedGeneration: 1,
        conditions: [{
          type: 'Ready', status: 'False', reason: 'Discovered',
          message: 'Discovery complete; waiting for spec',
          observedGeneration: 1, lastTransitionTime: '2026-07-24T09:53:08Z',
        }],
      },
    })
    expect(discovered.status).toBe('provisioning')
    expect(discovered.name).toBe('neura-k3s-w1')
    expect(discovered.serviceClass).toBe('MEDIUM')
  })

  it('reads online and degraded off the same reason', () => {
    const online = __testing.crToNode({
      metadata: { name: 'w2' },
      spec: { ip: '10.0.0.2' },
      status: { conditions: [{ type: 'Ready', status: 'True', reason: 'Online' }] },
    })
    expect(online.status).toBe('online')

    const degraded = __testing.crToNode({
      metadata: { name: 'w3' },
      spec: { ip: '10.0.0.3' },
      status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Degraded' }] },
    })
    expect(degraded.status).toBe('degraded')
  })

  it('reads a reason outside the frozen vocabulary as offline, not provisioning', () => {
    // v1beta1 froze the reasons at Discovered|Provisioning|Online|Degraded|
    // Offline. Anything else is a controller bug, and offline is the answer
    // that does not claim work is in progress that is not.
    const unknown = __testing.crToNode({
      metadata: { name: 'w4' },
      spec: { ip: '10.0.0.4' },
      status: { conditions: [{ type: 'Ready', status: 'Unknown', reason: 'Discovering' }] },
    })
    expect(unknown.status).toBe('offline')

    const unreconciled = __testing.crToNode({ metadata: { name: 'w5' }, spec: { ip: '10.0.0.5' } })
    expect(unreconciled.status).toBe('offline')
  })

  it('leaves serviceClass empty rather than inventing a tier the CRD does not default', () => {
    const unclassified = __testing.crToNode({
      metadata: { name: 'w6' },
      spec: { ip: '10.0.0.6' },
      status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Discovered' }] },
    })
    expect(unclassified.serviceClass).toBe('')
  })
})

/**
 * The SDK reads `window.__FRAME_NAMESPACE__` and `window.__FRAME_TOKEN__`, and
 * the vitest environment is `node`, which has no `window`. Stub one so the
 * client-level tests below exercise the real request path.
 */
function stubBrowser(overrides: Record<string, string> = {}) {
  vi.stubGlobal('window', overrides)
}

describe('NodeClient.getStatus', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  /** Serve one FrameNode CR and record the URL it was asked for. */
  function serve(cr: unknown): { urls: string[] } {
    stubBrowser()
    const urls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      urls.push(String(input))
      return new Response(JSON.stringify(cr), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    return { urls }
  }

  it('still reports a phase string for the provisioning wizard, taken from the condition', async () => {
    // NodeProvisionWizard polls this for 'Discovered' and 'Online'. It reads a
    // field that no longer exists on the wire, so the whole wizard hangs on
    // this projection being here rather than in the CR.
    const { urls } = serve({
      metadata: { name: 'neura-k3s-w1', namespace: 'default' },
      spec: { ip: '192.168.2.202' },
      status: {
        discoveredHostname: 'neura-k3s-w1',
        discoveredTalosVersion: 'v1.9.1',
        discoveredDisks: [{ name: '/dev/nvme0n1', size: '512Gi', type: 'nvme' }],
        conditions: [{ type: 'Ready', status: 'False', reason: 'Discovered' }],
      },
    })

    const status = await createFrameClient().nodes.getStatus('neura-k3s-w1')

    expect(status.phase).toBe('Discovered')
    expect(status.discoveredHostname).toBe('neura-k3s-w1')
    expect(status.discoveredDisks).toHaveLength(1)
    // And it asked the hub version, not the deprecated spoke.
    expect(urls[0]).toContain('/apis/frame.plume-labs.io/v1beta1/')
    expect(urls[0]).not.toContain('v1alpha1')
  })

  it('reports an empty phase for an unreconciled node instead of a stale one', async () => {
    serve({ metadata: { name: 'w9', namespace: 'default' }, spec: { ip: '10.0.0.9' }, status: {} })
    expect((await createFrameClient().nodes.getStatus('w9')).phase).toBe('')
  })
})

describe('JobClient.submit', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  /** Echo the posted body back as the created object, and record the request. */
  function capture(): { sent: Array<{ url: string; body: Record<string, unknown> }> } {
    stubBrowser()
    const sent: Array<{ url: string; body: Record<string, unknown> }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>
      sent.push({ url: String(input), body })
      return new Response(String(init?.body), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }))
    return { sent }
  }

  it('sends neither spec.namespace nor a serviceClass the caller did not choose', async () => {
    const { sent } = capture()

    await createFrameClient().jobs.submit({ name: 'llm-run-4', pipeline: 'neura-training-dag' })

    const spec = (sent[0].body as { spec: Record<string, unknown> }).spec
    expect(spec).toEqual({ pipeline: 'neura-training-dag', gpuCount: 0 })
    expect(spec).not.toHaveProperty('namespace')
    expect(spec).not.toHaveProperty('serviceClass')
    expect(spec).not.toHaveProperty('priority')
  })

  it('creates the FrameJob in the requested namespace, since that is where its workflow runs', async () => {
    // Under v1alpha1 this went into the client namespace with spec.namespace
    // pointing at team-a. With spec.namespace gone the only way to keep the
    // workflow in team-a is to create the FrameJob there — which is exactly
    // the privilege F5 stopped handing out for free.
    const { sent } = capture()

    const job = await createFrameClient().jobs.submit({
      name: 'llm-run-5', pipeline: 'neura-training-dag', namespace: 'team-a', serviceClass: 'HIGH',
    })

    expect(sent[0].url).toBe('/apis/frame.plume-labs.io/v1beta1/namespaces/team-a/framejobs')
    expect((sent[0].body as { metadata: { namespace: string } }).metadata.namespace).toBe('team-a')
    expect((sent[0].body as { spec: { serviceClass: string } }).spec.serviceClass).toBe('HIGH')
    expect(job.namespace).toBe('team-a')
  })
})

describe('k8sFetch in-flight de-duplication', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  /**
   * Serve a node list, but only resolve when the test says so, so both callers
   * are genuinely concurrent rather than one finishing before the other starts.
   */
  function gatedNodeList() {
    stubBrowser()
    let release!: () => void
    const gate = new Promise<void>((r) => { release = r })
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(`${init?.method ?? 'GET'} ${String(input)}`)
      await gate
      return new Response(JSON.stringify({ items: [], metadata: {} }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    }))
    return { calls, release }
  }

  it('issues one request when two screens read the same path at once', async () => {
    // The real shape of the problem: many mounted components each watch nodes
    // and each re-read on the same event. A broken version reaches the network
    // twice here — which at the observed fan-out was 321 reads in 20 seconds.
    const { calls, release } = gatedNodeList()
    const frame = createFrameClient()

    const both = Promise.all([frame.cluster.nodes(), frame.cluster.nodes()])
    release()
    await both

    expect(calls.filter((c) => c.includes('/api/v1/nodes')).length).toBe(1)
  })

  it('reaches the network again once the first request has settled', async () => {
    // De-duplication, not caching: the entry must not outlive the request, or
    // a screen would keep rendering data from before the last write.
    const { calls, release } = gatedNodeList()
    const frame = createFrameClient()

    const first = frame.cluster.nodes()
    release()
    await first
    await frame.cluster.nodes()

    expect(calls.filter((c) => c.includes('/api/v1/nodes')).length).toBe(2)
  })

  it('never collapses two writes to one path', async () => {
    // Two cordons are not interchangeable the way two reads are; dropping one
    // would silently lose a user action.
    stubBrowser()
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(`${init?.method ?? 'GET'} ${String(input)}`)
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const frame = createFrameClient()

    await Promise.all([frame.cluster.cordon('w1', true), frame.cluster.cordon('w1', false)])

    expect(calls.filter((c) => c.includes('/api/v1/nodes/w1')).length).toBe(2)
  })
})

describe('k8sFetchUncached 401 retry', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  /**
   * `window` aliased to `globalThis`, the same way a real browser has them be
   * the same object. `auth.ts` publishes the refreshed token onto
   * `globalThis.__FRAME_TOKEN__`; `bearerToken()` reads it off `window`. A
   * plain stub object for `window` (as `stubBrowser()` above uses) would
   * silently decouple the two and let a broken retry pass by never actually
   * checking what token the retried request carried.
   */
  function stubBrowserAliasedToGlobalThis() {
    vi.stubGlobal('window', globalThis)
  }

  /**
   * Fetch stub shared by the tests below: `/auth/token` mints
   * `opts.tokenValue`, and `path` answers 401 for its first
   * `opts.unauthorizedTimes` hits and 200 after. Records every call's
   * method+URL and the Authorization header it carried, so a test can assert
   * both how many requests were made and what token the retry actually sent.
   */
  function stub401Retry(path: string, opts: { unauthorizedTimes: number; tokenValue?: string }) {
    stubBrowserAliasedToGlobalThis()
    const calls: string[] = []
    const authHeaders: Array<string | undefined> = []
    let pathHits = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      calls.push(`${init?.method ?? 'GET'} ${url}`)
      authHeaders.push((init?.headers as Record<string, string> | undefined)?.Authorization)
      if (url === '/auth/token') {
        return new Response(
          JSON.stringify({ id_token: opts.tokenValue ?? 'fresh', expires_in: 900 }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      pathHits += 1
      if (pathHits <= opts.unauthorizedTimes) {
        return new Response(JSON.stringify({ message: 'unauthorized' }), {
          status: 401, headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    return { calls, authHeaders }
  }

  it('retries once and succeeds when the refresh returns a new token', async () => {
    const { calls, authHeaders } = stub401Retry('/api/v1/nodes/w1', { unauthorizedTimes: 1 })

    await createFrameClient().cluster.cordon('w1', true)

    // Fails if the retry is removed (only one nodes call) and fails if the
    // retry becomes unbounded (more than two).
    expect(calls.filter((c) => c.includes('/api/v1/nodes/w1')).length).toBe(2)
    expect(calls.filter((c) => c === 'POST /auth/token').length).toBe(1)
    // The retry must actually carry the refreshed token.
    expect(authHeaders[authHeaders.length - 1]).toBe('Bearer fresh')
  })

  it('gives up after a second consecutive 401 instead of retrying again', async () => {
    const { calls } = stub401Retry('/api/v1/nodes/w1', {
      unauthorizedTimes: Infinity,
      tokenValue: 'still-rejected',
    })

    await expect(createFrameClient().cluster.cordon('w1', true)).rejects.toThrow(FrameAPIError)

    // Exactly one retry — two attempts against the apiserver, never a third.
    expect(calls.filter((c) => c.includes('/api/v1/nodes/w1')).length).toBe(2)
    expect(calls.filter((c) => c === 'POST /auth/token').length).toBe(1)
  })

  // I1 of the whole-branch review. The proxy replied `http.Error(w,
  // "unauthorized", 401)` — text/plain — and `parseApiserverResponse` called
  // `res.json()` on every response including the 401 fall-through, so the
  // caller got `SyntaxError: Unexpected token 'u'` and never a
  // FrameAPIError(401). Nothing could tell "session gone" from a bug.
  //
  // The proxy now answers with a metav1.Status. This asserts the client
  // survives a body that is not JSON anyway — an ingress 502, an nginx 504,
  // or any other hop that does not speak Kubernetes.
  it('turns a non-JSON error body into a FrameAPIError, not a parse error', async () => {
    stubBrowserAliasedToGlobalThis()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/auth/token') {
        return new Response('unauthorized', { status: 401 })
      }
      return new Response('unauthorized', {
        status: 401, headers: { 'Content-Type': 'text/plain' },
      })
    }))

    const err = await createFrameClient().cluster.cordon('w1', true).catch((e) => e)
    expect(err).toBeInstanceOf(FrameAPIError)
    expect((err as FrameAPIError).statusCode).toBe(401)
    expect((err as FrameAPIError).message).toContain('unauthorized')
  })

  it('reports a non-JSON 5xx as its status rather than as a parse error', async () => {
    stubBrowserAliasedToGlobalThis()
    vi.stubGlobal('fetch', vi.fn(async () =>
      new Response('<html>502 Bad Gateway</html>', {
        status: 502, headers: { 'Content-Type': 'text/html' },
      })))

    const err = await createFrameClient().cluster.nodes().catch((e) => e)
    expect(err).toBeInstanceOf(FrameAPIError)
    expect((err as FrameAPIError).statusCode).toBe(502)
  })

  it('forces a fresh token on retry rather than reusing one that still has time left', async () => {
    // Pre-populate a session with 15 minutes left — comfortably outside
    // ensureToken's 2-minute refresh margin. A retry built on ensureToken()
    // would reuse this cached token and never touch the network again, even
    // though it is exactly the token the apiserver just rejected.
    vi.stubGlobal('fetch', vi.fn(async () =>
      new Response(JSON.stringify({ id_token: 'cached', expires_in: 900 }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })))
    await currentSession()

    const { calls, authHeaders } = stub401Retry('/api/v1/nodes/w1', { unauthorizedTimes: 1 })

    await createFrameClient().cluster.cordon('w1', true)

    expect(calls.filter((c) => c === 'POST /auth/token').length).toBe(1)
    expect(authHeaders[authHeaders.length - 1]).toBe('Bearer fresh')
  })
})

describe('ClusterClient.capacity', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  const NODES = { items: [{ status: { allocatable: { cpu: '8', memory: '32Gi' } } }] }

  /**
   * Route by URL. `promValues` null makes the Prometheus instant query fail the
   * way an undeployed or unreachable Prometheus does.
   */
  function serve(promValues: [number, number] | null) {
    stubBrowser()
    const urls: string[] = []
    let promCall = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      urls.push(url)
      const json = (o: unknown, status = 200) =>
        new Response(JSON.stringify(o), { status, headers: { 'Content-Type': 'application/json' } })

      if (url.includes('/proxy/api/v1/query')) {
        if (!promValues) return new Response('nope', { status: 503 })
        const v = promValues[promCall++]
        return json({ data: { result: [{ value: [0, String(v)] }] } })
      }
      // Prometheus pod discovery, before the query itself.
      if (url.includes('/namespaces/monitoring/pods')) return json({ items: [{ metadata: { name: 'prom-0' } }] })
      if (url.includes('/api/v1/nodes')) return json(NODES)
      if (url.includes('/apis/metrics.k8s.io')) return json({ items: [] })
      if (url.includes('/api/v1/pods')) {
        return json({ items: [
          { status: { phase: 'Running' }, spec: { containers: [{ resources: { requests: { cpu: '500m', memory: '1Gi' } } }] } },
          // Must be skipped: a finished pod asks nothing of the scheduler.
          { status: { phase: 'Succeeded' }, spec: { containers: [{ resources: { requests: { cpu: '4', memory: '8Gi' } } }] } },
        ] })
      }
      return json({ items: [] })
    }))
    return { urls }
  }

  it('does not read the whole pod list when Prometheus can answer', async () => {
    // The point of the change: 432 KB per call, on every screen, every 30 s.
    const { urls } = serve([4.965, 15.244])

    const cap = await createFrameClient().cluster.capacity()

    expect(urls.some((u) => u.includes('/api/v1/pods'))).toBe(false)
    expect(cap.find((c) => c.name === 'CPU')?.requested).toBeCloseTo(4.965, 3)
    expect(cap.find((c) => c.name === 'Memory')?.requested).toBeCloseTo(15.244, 3)
  })

  it('falls back to the pod list when Prometheus is unavailable', async () => {
    // Without the fallback an absent Prometheus would silently show 0 requested.
    const { urls } = serve(null)

    const cap = await createFrameClient().cluster.capacity()

    expect(urls.some((u) => u.includes('/api/v1/pods'))).toBe(true)
    expect(cap.find((c) => c.name === 'CPU')?.requested).toBeCloseTo(0.5, 3)
    expect(cap.find((c) => c.name === 'Memory')?.requested).toBeCloseTo(1, 3)
  })
})

// I2 of the whole-branch review. `internal/uiproxy/recorder.go` has read
// `X-Frame-Action` since the first commit, and a repo-wide grep found the
// header nowhere but in Go tests and doc comments — `sendToApiserver` set
// only Authorization and Content-Type. So every FrameTask fell back to
// `${verb} ${target}`: "patch nodes/w2", the same string for a cordon and an
// uncordon, and the Tasks screen the lot is named for showed the request
// instead of the action.
describe('X-Frame-Action', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  function captureHeaders() {
    vi.stubGlobal('window', globalThis)
    const seen: Array<Record<string, string> | undefined> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(init?.headers as Record<string, string> | undefined)
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    return seen
  }

  it('labels a cordon and an uncordon differently', async () => {
    const seen = captureHeaders()
    await createFrameClient().cluster.cordon('w2', true)
    await createFrameClient().cluster.cordon('w2', false)
    expect(seen[0]?.['X-Frame-Action']).toBe('cordon node w2')
    expect(seen[1]?.['X-Frame-Action']).toBe('uncordon node w2')
  })

  it('labels a scale with its replica count', async () => {
    const seen = captureHeaders()
    await createFrameClient().apps.scale(
      { kind: 'Deployment', namespace: 'inference', name: 'llamacpp' }, 3)
    expect(seen[0]?.['X-Frame-Action']).toBe('scale deployment inference/llamacpp to 3')
  })

  it('sends no header on a read, since reads are not recorded', async () => {
    const seen = captureHeaders()
    await createFrameClient().nodes.list()
    expect(seen[0]?.['X-Frame-Action']).toBeUndefined()
  })

  it('strips characters a header cannot carry', async () => {
    // Action labels are assembled from names a user chose. A non-latin-1
    // byte in a header value makes fetch() throw, which would turn a
    // cosmetic label into a failed write.
    vi.stubGlobal('window', globalThis)
    const seen: Array<Record<string, string> | undefined> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(init?.headers as Record<string, string> | undefined)
      return new Response(JSON.stringify({ metadata: { name: 'j' }, spec: {} }),
        { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    await createFrameClient().jobs.submit({ name: 'train-\u00e9\u00e9\u2014\ud83d\ude80', image: 'x' } as never)
    expect(seen[0]?.['X-Frame-Action']).toBe('submit job train-')
  })
})

describe('crToTask', () => {
  const { crToTask } = __testing

  it('maps a refused write to a failed task', () => {
    const t = crToTask({
      metadata: { name: 'task-abc' },
      spec: { user: 'bob@example.com', verb: 'patch', action: 'cordon node w2',
              target: { resource: 'nodes', name: 'w2' } },
      status: { phase: 'Failed', httpCode: 403, startedAt: '2026-09-08T10:00:00Z' },
    })
    expect(t.phase).toBe('Failed')
    expect(t.httpCode).toBe(403)
    expect(t.target).toBe('nodes/w2')
  })

  it('treats a task with no status as running', () => {
    const t = crToTask({
      metadata: { name: 'task-def' },
      spec: { user: 'a@b.c', verb: 'create', target: { resource: 'framejobs', namespace: 'frame-system', name: '-' } },
    })
    expect(t.phase).toBe('Running')
    expect(t.target).toBe('frame-system/framejobs/-')
  })

  it('normalizes a phase outside Running/Succeeded/Failed instead of passing it through', () => {
    // crToJob and crToNode both switch on the raw string with a safe
    // default; that's what lets TasksView do an unconditional
    // PHASE[t.phase] lookup. An unvalidated cast here would let any string
    // the controller ever emits reach that lookup unnormalized and throw
    // when PHASE[...] comes back undefined — blanking the whole console via
    // the top-level ErrorBoundary, not just the Tasks tab.
    const t = crToTask({
      metadata: { name: 'task-ghi' },
      spec: { user: 'x@y.z', verb: 'delete', target: { resource: 'nodes', name: 'w9' } },
      status: { phase: 'SomeFutureEnumValue' },
    })
    expect(t.phase).toBe('Running')
  })

  // The whole reason ObjectRef gained a subresource. Rendered without it, a
  // shell opened in a pod and a pod created from the console print the same
  // target, and the Tasks screen cannot tell one from the other.
  it('renders the subresource, so an exec reads as an exec', () => {
    const t = crToTask({
      metadata: { name: 'task-exec' },
      spec: {
        user: 'alice@example.com',
        verb: 'create',
        action: 'open a shell in neura/api-0 (api)',
        target: { resource: 'pods', namespace: 'neura', name: 'api-0', subresource: 'exec' },
      },
    })
    expect(t.target).toBe('neura/pods/api-0/exec')
  })
})

// Not in the whole-branch review's list, found while fixing I2: every
// integration panel reached its exporter through a bare `fetch()`.
//
// Those URLs are apiserver paths — `/api/v1/namespaces/{ns}/pods/{p}:{port}/proxy/...`
// — so they go through nginx to the uiproxy, which rejects any request
// without a bearer token before RBAC is ever consulted. Prometheus, Alluxio,
// node-exporter, DCGM, llama.cpp, TEI, Falco, Tetragon and Alertmanager
// would each have answered 401 for everyone, including admins, and no
// amount of fixing the RBAC (C3) would have changed it.
describe('integration proxy requests carry the bearer token', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  function stubTokenAnd(respond: (url: string) => Response) {
    vi.stubGlobal('window', globalThis)
    const seen: Array<{ url: string; auth?: string }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      seen.push({ url, auth: (init?.headers as Record<string, string> | undefined)?.Authorization })
      return respond(url)
    }))
    ;(globalThis as Record<string, unknown>).__FRAME_TOKEN__ = 'tok'
    return seen
  }

  const POD_LIST = JSON.stringify({ items: [{ metadata: { name: 'alertmanager-0' } }] })

  it('reads Alertmanager silences with an Authorization header', async () => {
    const seen = stubTokenAnd((url) =>
      url.includes('/proxy/')
        ? new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
        : new Response(POD_LIST, { status: 200, headers: { 'Content-Type': 'application/json' } }))

    await createFrameClient().cluster.silences()

    const proxied = seen.filter((c) => c.url.includes('/proxy/'))
    expect(proxied.length).toBeGreaterThan(0)
    for (const c of proxied) expect(c.auth).toBe('Bearer tok')
  })

  it('creates a silence with an Authorization header', async () => {
    const seen = stubTokenAnd((url) =>
      url.includes('/proxy/')
        ? new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
        : new Response(POD_LIST, { status: 200, headers: { 'Content-Type': 'application/json' } }))

    await createFrameClient().cluster.silenceAlert(
      { id: 'a', name: 'x', severity: 'warning', summary: '', startsAt: '', labels: { alertname: 'x' } } as never,
      30,
      'someone@example.com',
    )

    const proxied = seen.filter((c) => c.url.includes('/proxy/'))
    expect(proxied.length).toBeGreaterThan(0)
    for (const c of proxied) expect(c.auth).toBe('Bearer tok')
  })

  // The structural half. Twelve more call sites have the same shape and
  // stubbing each of their screens would be a lot of fixture for one
  // property: nothing in this module may reach the apiserver without going
  // through a helper that attaches the token.
  it('has no bare fetch() left in the module', () => {
    const bare = frameSdkSource
      .split('\n')
      .map((line: string, i: number) => ({ line, n: i + 1 }))
      // `globalThis.fetch` is the two helpers' own call; anything else
      // spelling `fetch(` is a call site that forgot the token.
      .filter(({ line }: { line: string }) =>
        /(?<![a-zA-Z.])fetch\(/.test(line) && !/proxyFetch\(|k8sFetch|^\s*\*/.test(line))
    expect(bare.map((b: { n: number; line: string }) => `${b.n}: ${b.line.trim()}`)).toEqual([])
  })
})

// Carried in from lot 0c's whole-branch review. `frame-uiproxy` creates every
// FrameTask in TASK_NAMESPACE — `frame-system`, from cmd/uiproxy/main.go and
// deploy/kubernetes/base/deployment.yaml — while this client built the path
// from `config().frameNamespace`, whose default is `default`. The Tasks screen
// has shown an empty list since it shipped: a 200 with `items: []`, no error,
// nothing to notice.
//
// The path is spelled out in full, namespace segment included. A
// `url.includes('/frametasks')` assertion passes just as happily against
// `/apis/.../namespaces/default/frametasks`, which is the bug — the same
// substring trap that let the Accounts screen ship pointed at the wrong
// namespace (see FRAMEUSERS_PATH in accounts.test.ts).
describe('FrameTask reads', () => {
  const FRAMETASKS_PATH =
    '/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/frametasks'

  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  it('reads from the namespace the recorder writes to', async () => {
    vi.stubGlobal('window', globalThis)
    const urls: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        urls.push(String(input))
        return new Response(JSON.stringify({ items: [] }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }),
    )

    await createFrameClient().tasks.list()

    expect(urls).toEqual([`${FRAMETASKS_PATH}?limit=200`])
  })
})
