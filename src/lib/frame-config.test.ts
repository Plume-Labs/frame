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
