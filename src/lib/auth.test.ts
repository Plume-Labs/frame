import { beforeEach, describe, expect, it, vi } from 'vitest'
import { __resetForTests, currentSession, ensureToken, logout } from '@/lib/auth'

const REFRESH_MARGIN_MS = 120_000

function mockToken(expiresIn: number) {
  return vi.fn(async () => new Response(JSON.stringify({ id_token: 't-' + expiresIn, expires_in: expiresIn }), { status: 200 }))
}

beforeEach(() => { __resetForTests() })

describe('session', () => {
  it('publishes the token where the SDK reads it', async () => {
    vi.stubGlobal('fetch', mockToken(900))
    await currentSession()
    expect((globalThis as Record<string, unknown>).__FRAME_TOKEN__).toBe('t-900')
  })

  it('returns undefined when the session cookie is gone', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('unauthorized', { status: 401 })))
    expect(await currentSession()).toBeUndefined()
  })

  it('does not refresh a token with time left', async () => {
    const f = mockToken(900)
    vi.stubGlobal('fetch', f)
    await currentSession()
    await ensureToken(Date.now() + 60_000)
    expect(f).toHaveBeenCalledTimes(1)
  })

  it('refreshes inside the margin', async () => {
    const f = mockToken(900)
    vi.stubGlobal('fetch', f)
    await currentSession()
    await ensureToken(Date.now() + 900_000 - REFRESH_MARGIN_MS + 1_000)
    expect(f).toHaveBeenCalledTimes(2)
  })

  it('clears the token on logout', async () => {
    vi.stubGlobal('fetch', mockToken(900))
    await currentSession()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))
    await logout()
    expect((globalThis as Record<string, unknown>).__FRAME_TOKEN__).toBeUndefined()
  })
})
