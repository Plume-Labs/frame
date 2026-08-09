import { describe, it, expect, vi, afterEach } from 'vitest'
import { __testing, createFrameClient, projectToFull, type MetricSeries } from './frame-sdk'

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
