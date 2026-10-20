import { describe, it, expect, vi, afterEach } from 'vitest'
import { config, DEFAULT_CONFIG, loadConfig } from './frame-config'

/**
 * `authHeaders()` in frame-config.ts reads a bare `window`, which does not
 * exist under vitest's `environment: 'node'` — referencing it throws a
 * ReferenceError before any assertion runs. Same stub every other suite that
 * reaches the SDK uses.
 */
function stubBrowser() {
  vi.stubGlobal('window', {})
}

function stubConfigMap(data: Record<string, string> | undefined) {
  stubBrowser()
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(JSON.stringify({ data }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        }),
    ),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('taskNamespace', () => {
  // The whole point of the field. frame-uiproxy's TASK_NAMESPACE defaults to
  // frame-system (cmd/uiproxy/main.go) and the shipped Deployment sets it to
  // frame-system, so a console that has never been configured must look there
  // — not at `default`, which is where frameNamespace points and where no
  // FrameTask has ever been written.
  it('defaults to frame-system, where the recorder writes', () => {
    expect(DEFAULT_CONFIG.taskNamespace).toBe('frame-system')
  })

  it('takes the stored value when the ConfigMap sets one', async () => {
    stubConfigMap({ 'config.json': JSON.stringify({ taskNamespace: 'frame-audit' }) })
    await loadConfig()
    expect(config().taskNamespace).toBe('frame-audit')
  })

  // A ConfigMap written before this field existed has no `taskNamespace` key.
  // `merge` must fall through to the default rather than let `undefined` reach
  // the path builder, which would produce
  // `/apis/.../namespaces/undefined/frametasks` — a 404 on every load.
  it('keeps the default when the stored config predates the field', async () => {
    stubConfigMap({ 'config.json': JSON.stringify({ frameNamespace: 'frame-system' }) })
    await loadConfig()
    expect(config().taskNamespace).toBe('frame-system')
  })
})

describe('storage.osdStableAfterMinutes', () => {
  it('prend une valeur positive du ConfigMap, et le défaut sinon', async () => {
    for (const [stored, want] of [
      [{ osdStableAfterMinutes: 15 }, 15],
      [{ osdStableAfterMinutes: 0 }, 60],
      [{ osdStableAfterMinutes: -5 }, 60],
      [{ osdStableAfterMinutes: 'soon' }, 60],
      [undefined, 60],
    ] as const) {
      stubConfigMap({ 'config.json': JSON.stringify({ storage: stored }) })
      await loadConfig()
      expect(config().storage.osdStableAfterMinutes).toBe(want)
    }
  })
})

describe('integrations.sandboxPods', () => {
  it('ships a selector that names no particular application', () => {
    expect(DEFAULT_CONFIG.integrations.sandboxPods.selector).toBe('app=sandbox')
  })

  // Only `sandboxPods` is read: a stored key the defaults do not define (such
  // as the 1.0 name of this integration) is ignored, never migrated, and
  // never surfaces in the merged config.
  it('ignores an integration key the defaults do not define', async () => {
    stubConfigMap({
      'config.json': JSON.stringify({ integrations: { legacySandbox: { namespace: '', selector: 'app=old' } } }),
    })
    await loadConfig()
    expect(config().integrations.sandboxPods.selector).toBe('app=sandbox')
    expect(Object.keys(config().integrations)).not.toContain('legacySandbox')
  })

  it('reads the selector stored under sandboxPods', async () => {
    stubConfigMap({
      'config.json': JSON.stringify({ integrations: { sandboxPods: { namespace: '', selector: 'app=new' } } }),
    })
    await loadConfig()
    expect(config().integrations.sandboxPods.selector).toBe('app=new')
  })
})

describe('workloads.extraOperableNamespaces', () => {
  it('is empty by default', () => {
    expect(DEFAULT_CONFIG.workloads.extraOperableNamespaces).toEqual([])
  })

  it('keeps namespace names and drops anything that is not one', async () => {
    stubConfigMap({
      'config.json': JSON.stringify({ workloads: { extraOperableNamespaces: ['team-a', 'Bad_Name', 42, 'team-b'] } }),
    })
    await loadConfig()
    expect(config().workloads.extraOperableNamespaces).toEqual(['team-a', 'team-b'])
  })

  it('keeps the default when the stored config predates the field', async () => {
    stubConfigMap({ 'config.json': JSON.stringify({ frameNamespace: 'frame-system' }) })
    await loadConfig()
    expect(config().workloads.extraOperableNamespaces).toEqual([])
  })
})
