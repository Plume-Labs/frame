/// <reference types="vite/client" />
import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  __testing,
  APPLICATION_WATCH_PATHS,
  createFrameClient,
  FrameAPIError,
  machinesPath,
  powerPatchBody,
  projectToFull,
  workloadWatchPaths,
  type CephStatus,
  type MetricSeries,
} from './frame-sdk'
import { __resetForTests as resetAuthForTests, currentSession } from './auth'
import { MAX_ACTION_LENGTH } from './manifest-diff'
import type { Machine } from './machines'
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

// R18: an earlier version of ceph() remapped status.ceph.details into a
// generic checks[code].summary.message shape for cephWarningReasons
// (storage.ts) to consume, and shipped with nothing here exercising that
// remap — cephWarningReasons was tested only against a shape the cluster
// never actually sends. The fix deletes the translation: ceph() now hands
// `details` straight through, and this pins that the pass-through actually
// happens, not just that the field compiles. A silent regression here (e.g.
// someone reintroducing a remap, or dropping the field on a refactor) would
// otherwise only be caught by noticing a WARN's reasons went blank on a
// live cluster.
describe('ClusterClient.ceph()', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  it('carries status.ceph.details through verbatim', async () => {
    vi.stubGlobal('window', globalThis)
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input)
        if (url.includes('/cephclusters')) {
          return new Response(
            JSON.stringify({
              items: [
                {
                  status: {
                    ceph: {
                      health: 'HEALTH_WARN',
                      details: {
                        POOL_NO_REDUNDANCY: {
                          message: '1 pool(s) have no replicas configured',
                          severity: 'HEALTH_WARN',
                        },
                        MON_CLOCK_SKEW: {
                          message: 'clock skew detected on mon.b',
                          severity: 'HEALTH_WARN',
                        },
                      },
                    },
                  },
                },
              ],
            }),
            { status: 200, headers: { 'content-type': 'application/json' } },
          )
        }
        // cephblockpools and the osd/mon pod lists: empty is enough, this
        // test is only about the details pass-through above.
        return new Response(JSON.stringify({ items: [] }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }),
    )

    const status: CephStatus = await createFrameClient().cluster.ceph()

    expect(status.details).toEqual({
      POOL_NO_REDUNDANCY: { message: '1 pool(s) have no replicas configured', severity: 'HEALTH_WARN' },
      MON_CLOCK_SKEW: { message: 'clock skew detected on mon.b', severity: 'HEALTH_WARN' },
    })
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

// Every path is spelled out in full. A `url.includes('/deployments')` test
// passes against `/apis/apps/v1/namespaces/default/deployments`, which is the
// exact failure the Accounts screen shipped with — a list that comes back
// empty, with a 200 and no error to notice.
describe('WorkloadClient', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    resetAuthForTests()
  })

  interface Seen {
    url: string
    method: string
    headers: Record<string, string>
    body?: string
    signal?: AbortSignal | null
  }

  function capture(respond: (url: string) => Response): Seen[] {
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
          signal: init?.signal,
        })
        return respond(url)
      }),
    )
    return seen
  }

  const json = (body: unknown) =>
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    })

  it('reads the five collections the tree is built from, cluster-wide', async () => {
    const seen = capture(() => json({ items: [] }))
    await createFrameClient().workloads.tree()
    expect(seen.map((s) => s.url).sort()).toEqual([
      '/api/v1/pods',
      '/apis/apps/v1/daemonsets',
      '/apis/apps/v1/deployments',
      '/apis/apps/v1/replicasets',
      '/apis/apps/v1/statefulsets',
      '/apis/batch/v1/jobs',
    ])
  })

  it("resolves a component's pods through its own selector, not through every pod in the namespace", async () => {
    const seen = capture((url) => {
      if (url.startsWith('/apis/apps/v1/namespaces/neura/deployments/api')) {
        return json({ spec: { selector: { matchLabels: { app: 'api', tier: 'web' } } } })
      }
      return json({ items: [{ metadata: { name: 'api-1', namespace: 'neura' }, spec: { containers: [{ name: 'api' }] } }] })
    })
    const pods = await createFrameClient().apps.pods({ kind: 'Deployment', namespace: 'neura', name: 'api' })
    expect(pods.map((p) => p.name)).toEqual(['api-1'])
    expect(seen.map((s) => s.url)).toContain(
      `/api/v1/namespaces/neura/pods?labelSelector=${encodeURIComponent('app=api,tier=web')}`,
    )
  })

  // A workload with no selector must return nothing rather than fall back to
  // an unfiltered list: an unfiltered list is every pod in the namespace, and
  // this panel would then present a neighbouring application's output as this
  // component's, which is worse than showing none.
  it('returns no pods, rather than the whole namespace, when the workload has no selector', async () => {
    const seen = capture(() => json({ spec: {} }))
    const pods = await createFrameClient().apps.pods({ kind: 'StatefulSet', namespace: 'neura', name: 'db' })
    expect(pods).toEqual([])
    expect(seen.map((s) => s.url).some((u) => u.includes('/pods'))).toBe(false)
  })

  // The contract this asserts is between two pieces of data, not a snapshot of
  // either: whatever `apps.list()` reads is what ApplicationsView has to
  // watch, and APPLICATION_WATCH_PATHS is the single place that pair is
  // written down. It goes red if a third collection is ever fetched without
  // being watched -- a screen watching Deployments but not StatefulSets would
  // sit frozen through a database scaling while tracking its API perfectly,
  // and nothing about it would look wrong.
  it('reads exactly the collections an applications screen is told to watch', async () => {
    const seen = capture(() => json({ items: [] }))
    await createFrameClient().apps.list()
    expect(seen.map((s) => s.url).sort()).toEqual([...APPLICATION_WATCH_PATHS].sort())
  })

  it('attaches a Deployment pod through its ReplicaSet', async () => {
    const seen = capture((url) => {
      if (url === '/apis/apps/v1/deployments') {
        return json({
          items: [
            {
              metadata: { name: 'api', namespace: 'neura' },
              spec: { replicas: 2 },
              status: { readyReplicas: 2 },
            },
          ],
        })
      }
      if (url === '/apis/apps/v1/replicasets') {
        return json({
          items: [
            {
              metadata: {
                name: 'api-7d9f8',
                namespace: 'neura',
                ownerReferences: [{ kind: 'Deployment', name: 'api', controller: true }],
              },
            },
          ],
        })
      }
      if (url === '/api/v1/pods') {
        return json({
          items: [
            {
              metadata: {
                name: 'api-7d9f8-x1',
                namespace: 'neura',
                ownerReferences: [{ kind: 'ReplicaSet', name: 'api-7d9f8', controller: true }],
              },
              spec: { nodeName: 'w2', containers: [{ name: 'api' }] },
              status: { phase: 'Running', containerStatuses: [{ restartCount: 3 }] },
            },
          ],
        })
      }
      return json({ items: [] })
    })

    const tree = await createFrameClient().workloads.tree()
    expect(seen.length).toBe(6)
    expect(tree).toHaveLength(1)
    expect(tree[0].controllers[0].controller.name).toBe('api')
    expect(tree[0].controllers[0].pods.map((p) => p.name)).toEqual(['api-7d9f8-x1'])
    expect(tree[0].controllers[0].pods[0].restarts).toBe(3)
    expect(tree[0].barePods).toEqual([])
  })

  // Task 8's review could not verify this, because it lives in this file:
  // `scalable` must come from the kind, not from whether a replica-shaped
  // field happens to be present on the object. A DaemonSet has no `scale`
  // subresource even when its object carries `spec.replicas` — an
  // implementation that read `cr.spec?.replicas !== undefined` instead of
  // switching on `kind` would pass every Deployment-only test in this file
  // and still offer a scale button that 404s.
  it('derives scalable from the kind, not from a replica-shaped field on the object', async () => {
    const seen = capture((url) => {
      if (url === '/apis/apps/v1/daemonsets') {
        return json({
          items: [
            {
              metadata: { name: 'node-exporter', namespace: 'monitoring' },
              // A real DaemonSet carries no `spec.replicas` — but nothing
              // stops an object from having one, and a wrong implementation
              // keyed off its presence would read this and say `true`.
              spec: { replicas: 3 },
              status: { desiredNumberScheduled: 3, numberReady: 3 },
            },
          ],
        })
      }
      return json({ items: [] })
    })

    const tree = await createFrameClient().workloads.tree()
    expect(seen.length).toBe(6)
    const daemonset = tree
      .flatMap((n) => n.controllers)
      .find((c) => c.controller.kind === 'DaemonSet')
    expect(daemonset?.controller.scalable).toBe(false)
  })

  it('restarts by patching the pod template annotation, and says so', async () => {
    const seen = capture(() => json({}))
    await createFrameClient().workloads.restart('Deployment', 'neura', 'api')
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api')
    expect(seen[0].method).toBe('PATCH')
    expect(seen[0].headers['Content-Type']).toBe('application/strategic-merge-patch+json')
    expect(seen[0].headers['X-Frame-Action']).toBe('restart deployment neura/api')
    expect(seen[0].body).toContain('kubectl.kubernetes.io/restartedAt')
  })

  // Kubernetes allows a 63-character namespace and, for most kinds, a
  // 253-character object name — a raw template literal for the label
  // exceeds FrameTaskSpec.Action's 200-character CRD cap for names nowhere
  // near this extreme. Past the cap the apiserver rejects the FrameTask
  // create outright: the recorder logs the error and the restart still
  // succeeds, so an unbounded label costs the audit record entirely, not
  // just its tail. The length assertion alone would pass a builder that
  // returned an empty string, so this also pins that the verb and the
  // object's identity survive the cap.
  it('caps the restart action label so a long name still gets an audit record', async () => {
    const seen = capture(() => json({}))
    const namespace = 'a'.repeat(63)
    const name = 'b'.repeat(253)

    await createFrameClient().workloads.restart('StatefulSet', namespace, name)

    const action = seen[0].headers['X-Frame-Action']
    expect(action.length).toBeLessThanOrEqual(MAX_ACTION_LENGTH)
    expect(action).toContain('restart statefulset')
    expect(action).toContain(namespace)
  })

  it('scales through the scale subresource, naming the target count', async () => {
    const seen = capture(() => json({}))
    await createFrameClient().workloads.scale('StatefulSet', 'neura', 'postgres', 0)
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/statefulsets/postgres/scale')
    expect(seen[0].method).toBe('PATCH')
    // Scaling to zero is a stop, and the label has to be able to say so — a
    // falsy check on `replicas` would drop the number and record "scale … to".
    expect(seen[0].headers['X-Frame-Action']).toBe('scale statefulset neura/postgres to 0')
    expect(seen[0].body).toBe('{"spec":{"replicas":0}}')
  })

  it('deletes one pod by name', async () => {
    const seen = capture(() => json({}))
    await createFrameClient().workloads.deletePod('neura', 'api-7d9f8-x1')
    expect(seen[0].url).toBe('/api/v1/namespaces/neura/pods/api-7d9f8-x1')
    expect(seen[0].method).toBe('DELETE')
    expect(seen[0].headers['X-Frame-Action']).toBe('delete pod neura/api-7d9f8-x1')
  })

  it('asks the apiserver for YAML rather than serialising any here', async () => {
    const seen = capture(
      () => new Response('kind: Deployment\n', { status: 200, headers: { 'content-type': 'application/yaml' } }),
    )
    const text = await createFrameClient().workloads.manifest('Deployment', 'neura', 'api')
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api')
    expect(seen[0].headers['Accept']).toBe('application/yaml')
    expect(text).toBe('kind: Deployment\n')
  })

  // The two-request shape is what makes the audit label possible without a
  // YAML parser: the dry run is the apiserver telling us what it would store,
  // and the diff is computed against that. Collapse it to one request and the
  // label can only ever be "update deployments/api" — the same string for a
  // replica bump and for adding a hostPath volume.
  it('validates the edit, then writes it with the fields that changed', async () => {
    const before = { metadata: { name: 'api', resourceVersion: '7' }, spec: { replicas: 2 } }
    const after = { metadata: { name: 'api', resourceVersion: '7' }, spec: { replicas: 5 } }
    const seen = capture(() => json(after))

    await createFrameClient().workloads.applyManifest({
      kind: 'Deployment',
      namespace: 'neura',
      name: 'api',
      before,
      yaml: 'spec:\n  replicas: 5\n',
    })

    expect(seen).toHaveLength(2)
    expect(seen[0].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api?dryRun=All')
    expect(seen[0].method).toBe('PUT')
    expect(seen[0].headers['Content-Type']).toBe('application/yaml')
    // The dry run leaves no FrameTask (TaskRecorder.Start skips it), so a
    // label on it would be a label on nothing.
    expect(seen[0].headers['X-Frame-Action']).toBeUndefined()

    expect(seen[1].url).toBe('/apis/apps/v1/namespaces/neura/deployments/api')
    expect(seen[1].method).toBe('PUT')
    expect(seen[1].headers['X-Frame-Action']).toBe('edit deployment neura/api: spec.replicas')
    // The user's text, byte for byte — not a re-serialisation of anything.
    expect(seen[1].body).toBe('spec:\n  replicas: 5\n')
  })

  // A log stream is a read, so it carries no action — but it must still carry
  // the token. The uiproxy rejects a request with no bearer before RBAC is
  // ever consulted, which is how every integration panel in this console once
  // answered 401 for everyone (see the suite below this one).
  it('sends the log request to the right container of the right pod, with the token', async () => {
    const seen = capture(() => new Response('line\n', { status: 200 }))
    ;(globalThis as Record<string, unknown>).__FRAME_TOKEN__ = 'tok'

    await createFrameClient().workloads.logs({
      namespace: 'neura', pod: 'api-0', container: 'api', previous: true,
    })

    expect(seen[0].url).toBe('/api/v1/namespaces/neura/pods/api-0/log?container=api&previous=true')
    expect(seen[0].headers['Authorization']).toBe('Bearer tok')
    expect(seen[0].headers['X-Frame-Action']).toBeUndefined()
  })

  // A caller that abandons a followed log stream (switches pods, closes the
  // panel) has to actually close the connection, not just stop reading from
  // it — cancelling a reader does nothing for a request whose headers haven't
  // arrived yet, and leaves a `follow=true` apiserver watch open forever. The
  // only way to do that is to hand the caller's AbortSignal to `fetch` itself,
  // so this pins that it reaches the same request the URL and token do.
  it('forwards the caller-supplied AbortSignal to the underlying fetch', async () => {
    const seen = capture(() => new Response('line\n', { status: 200 }))
    ;(globalThis as Record<string, unknown>).__FRAME_TOKEN__ = 'tok'
    const controller = new AbortController()

    await createFrameClient().workloads.logs(
      { namespace: 'neura', pod: 'api-0', container: 'api', follow: true },
      controller.signal,
    )

    expect(seen[0].signal).toBe(controller.signal)
  })
})

describe('workloadWatchPaths', () => {
  // Whole-branch review Important 7: this used to include the pod collection,
  // so cluster-wide pod churn (Argo workflows, Jobs, evictions) re-triggered
  // WorkloadsView's tree() — six unpaginated cluster-wide GETs including every
  // ReplicaSet — every time any pod anywhere changed. `Pod` is gone; the four
  // controller collections stay, because losing one of those would mean the
  // tree stops refreshing when a Deployment or Job actually changes.
  it('watches the four controller collections and not pods', () => {
    const paths = workloadWatchPaths()
    expect(paths).toEqual([
      '/apis/apps/v1/deployments',
      '/apis/apps/v1/statefulsets',
      '/apis/apps/v1/daemonsets',
      '/apis/batch/v1/jobs',
    ])
    expect(paths).not.toContain('/api/v1/pods')
  })
})

// The brief for this client's test read `machinesPath()` (no namespace) as
// `/apis/frame.plume-labs.io/v1beta1/framemachines` — the cluster-wide list
// form. That contradicts both this file's own `frameListPath`/`apiBase`
// convention (every other resource — framenodes, framejobs,
// schedulingpolicies — resolves a bare call through `frameNs()` to the
// configured default namespace, never to an all-namespaces list) and the
// FrameMachine CRD itself (`scope: Namespaced`,
// config/crd/bases/frame.plume-labs.io_framemachines.yaml). `machinesPath`
// is implemented exactly as the brief's own Step 4 snippet specifies —
// `frameListPath('framemachines', ns)` — so these tests assert what that
// produces, not the brief's Step 2 literal.
describe('machinesPath', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('resolves a bare call through the configured Frame namespace, like every other Frame CR list', () => {
    stubBrowser()
    expect(machinesPath()).toBe('/apis/frame.plume-labs.io/v1beta1/namespaces/default/framemachines')
  })

  it('targets an explicit namespace without touching window, for a write against one machine', () => {
    expect(machinesPath('team-a')).toBe('/apis/frame.plume-labs.io/v1beta1/namespaces/team-a/framemachines')
  })
})

describe('powerPatchBody', () => {
  // The write is a timestamp, not a desired state — a body carrying only the
  // action would make a second press a no-op, and a restart inexpressible.
  it('builds a patch carrying both the action and the moment it was asked for', () => {
    const before = Date.now()
    const body = powerPatchBody('ForceRestart')
    const at = Date.parse(body.spec.powerRequest.requestedAt)
    expect(body.spec.powerRequest.action).toBe('ForceRestart')
    expect(at).toBeGreaterThanOrEqual(before)
    expect(at).toBeLessThanOrEqual(Date.now())
  })
})

describe('MachineClient', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('lists machines mapped through toMachine, sorted by name', async () => {
    stubBrowser()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({
        items: [
          { metadata: { name: 'w2', namespace: 'default' } },
          { metadata: { name: 'w10', namespace: 'default' } },
          { metadata: { name: 'w1', namespace: 'default' } },
        ],
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )))

    const machines = await createFrameClient().machines.list()

    expect(machines.map((m) => m.name)).toEqual(['w1', 'w10', 'w2'])
    // toMachine ran: a field it derives (not present on the wire) is set.
    expect(machines[0].reachable).toBe(false)
  })

  it('watches the same collection it reads, for useLiveResource', () => {
    stubBrowser()
    expect(createFrameClient().machines.watchPath()).toBe(machinesPath())
  })

  it('writes the power request as a merge-patch, with the action bounded and dated through machines.ts', async () => {
    stubBrowser()
    const calls: Array<{ url: string; init?: RequestInit }> = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init })
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))

    const machine = { name: 'ml350-g9', namespace: 'default' } as Machine
    await createFrameClient().machines.power(machine, 'ForceRestart')

    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe(
      '/apis/frame.plume-labs.io/v1beta1/namespaces/default/framemachines/ml350-g9',
    )
    expect(calls[0].init?.method).toBe('PATCH')
    const headers = calls[0].init?.headers as Record<string, string>
    expect(headers['Content-Type']).toBe('application/merge-patch+json')
    expect(headers['X-Frame-Action']).toBe('power: ForceRestart ml350-g9')

    const body = JSON.parse(String(calls[0].init?.body)) as {
      spec: { powerRequest: { action: string; requestedAt: string } }
    }
    expect(body.spec.powerRequest.action).toBe('ForceRestart')
    expect(Date.parse(body.spec.powerRequest.requestedAt)).not.toBeNaN()
  })

  // The action header is capped at MAX_ACTION_LENGTH by `powerActionLabel`
  // (machines.ts) before it ever reaches `k8sFetch`. Past that cap the
  // uiproxy's FrameTask create is rejected outright and the write still
  // succeeds — so the record of who asked for it disappears silently. This
  // proves the cap is actually wired to this call site, not just present in
  // machines.ts and unused here.
  it('bounds the action header instead of silently losing the audit record on a long name', async () => {
    stubBrowser()
    const calls: Array<{ init?: RequestInit }> = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ init })
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))

    const machine = { name: 'm'.repeat(400), namespace: 'default' } as Machine
    await createFrameClient().machines.power(machine, 'ForceOff')

    const headers = calls[0].init?.headers as Record<string, string>
    expect(headers['X-Frame-Action'].length).toBe(MAX_ACTION_LENGTH)
  })
})
