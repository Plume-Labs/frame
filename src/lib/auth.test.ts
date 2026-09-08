import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  __resetForTests,
  currentSession,
  ensureToken,
  enrolPasskey,
  loginWithPasskey,
  loginWithPassword,
  AuthUnreachableError,
  logout,
  PasskeyCancelledError,
} from '@/lib/auth'

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

// --- WebAuthn -----------------------------------------------------------

/** `Response.json()`/`.text()` on a routed mock, dispatched by request URL+method. */
function routedFetch(routes: Record<string, () => Response>) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = init?.method ?? 'GET'
    const key = `${method} ${url}`
    for (const [pattern, respond] of Object.entries(routes)) {
      if (key === pattern || key.startsWith(pattern)) return respond()
    }
    throw new Error(`unexpected fetch: ${key}`)
  })
}

function mockAssertionCredential() {
  const response = {
    clientDataJSON: new Uint8Array([1, 1, 1]).buffer,
    authenticatorData: new Uint8Array([2, 2, 2]).buffer,
    signature: new Uint8Array([3, 3, 3]).buffer,
    userHandle: new Uint8Array([9, 9]).buffer,
  }
  return {
    id: 'cred-id',
    type: 'public-key',
    rawId: new Uint8Array([4, 4, 4]).buffer,
    authenticatorAttachment: 'platform',
    response,
    getClientExtensionResults: () => ({}),
  } as unknown as PublicKeyCredential
}

function mockAttestationCredential() {
  const response = {
    clientDataJSON: new Uint8Array([1, 1, 1]).buffer,
    attestationObject: new Uint8Array([5, 5, 5]).buffer,
  }
  return {
    id: 'cred-id',
    type: 'public-key',
    rawId: new Uint8Array([4, 4, 4]).buffer,
    authenticatorAttachment: 'platform',
    response,
    getClientExtensionResults: () => ({}),
  } as unknown as PublicKeyCredential
}

/** Stubs a WebAuthn-capable browser: `navigator.credentials` + `PublicKeyCredential`. */
function stubWebAuthnSupport(overrides?: { get?: () => Promise<Credential | null>; create?: () => Promise<Credential | null> }) {
  vi.stubGlobal('navigator', {
    credentials: {
      get: overrides?.get ?? vi.fn(async () => mockAssertionCredential()),
      create: overrides?.create ?? vi.fn(async () => mockAttestationCredential()),
    },
  })
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredentialStub() {})
}

describe('loginWithPasskey', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('runs begin -> navigator.credentials.get -> finish, and posts the shaped assertion', async () => {
    const f = routedFetch({
      'POST /auth/login/begin': () =>
        new Response(JSON.stringify({ publicKey: { challenge: '_w' } }), { status: 200 }),
      'POST /auth/login/finish': () => new Response(null, { status: 204 }),
    })
    vi.stubGlobal('fetch', f)
    stubWebAuthnSupport()

    await loginWithPasskey()

    expect(f).toHaveBeenCalledTimes(2)
    const [beginUrl] = f.mock.calls[0] as [string]
    const [finishUrl, finishInit] = f.mock.calls[1] as [string, RequestInit]
    expect(beginUrl).toBe('/auth/login/begin')
    expect(finishUrl).toBe('/auth/login/finish')
    const body = JSON.parse(finishInit.body as string)
    expect(body.id).toBe('cred-id')
    expect(body.response.userHandle).toBeDefined()
  })

  it('rejects with PasskeyCancelledError when the browser prompt is dismissed', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({
        'POST /auth/login/begin': () =>
          new Response(JSON.stringify({ publicKey: { challenge: '_w' } }), { status: 200 }),
      }),
    )
    stubWebAuthnSupport({
      get: vi.fn(async () => {
        throw new DOMException('user dismissed the prompt', 'NotAllowedError')
      }),
    })

    await expect(loginWithPasskey()).rejects.toBeInstanceOf(PasskeyCancelledError)
  })

  it('rejects with PasskeyCancelledError when get() resolves null', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({
        'POST /auth/login/begin': () =>
          new Response(JSON.stringify({ publicKey: { challenge: '_w' } }), { status: 200 }),
      }),
    )
    stubWebAuthnSupport({ get: vi.fn(async () => null) })

    await expect(loginWithPasskey()).rejects.toBeInstanceOf(PasskeyCancelledError)
  })

  it('rejects when the browser has no WebAuthn support, without touching the network', async () => {
    const f = vi.fn()
    vi.stubGlobal('fetch', f)
    vi.stubGlobal('navigator', {})
    vi.stubGlobal('PublicKeyCredential', undefined)

    await expect(loginWithPasskey()).rejects.toThrow(/does not support passkeys/)
    expect(f).not.toHaveBeenCalled()
  })

  it('surfaces a non-2xx /auth/login/finish as a thrown error', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({
        'POST /auth/login/begin': () =>
          new Response(JSON.stringify({ publicKey: { challenge: '_w' } }), { status: 200 }),
        'POST /auth/login/finish': () => new Response('unauthorized', { status: 401 }),
      }),
    )
    stubWebAuthnSupport()

    await expect(loginWithPasskey()).rejects.toThrow(/passkey sign-in failed/)
  })
})

describe('enrolPasskey', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('runs begin -> navigator.credentials.create -> finish, with the label on the query string', async () => {
    const f = routedFetch({
      'POST /auth/register/begin': () =>
        new Response(JSON.stringify({ publicKey: { challenge: '_w', user: { id: '_w', name: 'a', displayName: 'a' }, rp: { id: 'x', name: 'x' }, pubKeyCredParams: [] } }), {
          status: 200,
        }),
      'POST /auth/register/finish': () => new Response(null, { status: 204 }),
    })
    vi.stubGlobal('fetch', f)
    stubWebAuthnSupport()

    await enrolPasskey('YubiKey 5C')

    expect(f).toHaveBeenCalledTimes(2)
    const [finishUrl, finishInit] = f.mock.calls[1] as [string, RequestInit]
    expect(finishUrl).toBe('/auth/register/finish?label=YubiKey%205C')
    const body = JSON.parse(finishInit.body as string)
    expect(body.id).toBe('cred-id')
  })

  it('reports a clear error when there is no active session', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({
        'POST /auth/register/begin': () => new Response('unauthorized', { status: 401 }),
      }),
    )
    stubWebAuthnSupport()

    await expect(enrolPasskey('key')).rejects.toThrow(/active session/)
  })

  it('rejects with PasskeyCancelledError when the browser prompt is dismissed', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({
        'POST /auth/register/begin': () =>
          new Response(JSON.stringify({ publicKey: { challenge: '_w', user: { id: '_w', name: 'a', displayName: 'a' }, rp: { id: 'x', name: 'x' }, pubKeyCredParams: [] } }), {
            status: 200,
          }),
      }),
    )
    stubWebAuthnSupport({
      create: vi.fn(async () => {
        throw new DOMException('dismissed', 'NotAllowedError')
      }),
    })

    await expect(enrolPasskey('key')).rejects.toBeInstanceOf(PasskeyCancelledError)
  })
})

// --- /auth/* actually reaching authd ------------------------------------
//
// The whole-branch review's C1: nothing routed /auth/ to authd, so every one
// of these calls fell through nginx's `location /` to `try_files ...
// /index.html` and came back 200 text/html. `res.json()` on that throws
// `SyntaxError: Unexpected token '<'`, which is what the login screen showed
// under a form that had just "succeeded".
//
// These two are the discriminating checks: an HTML body must read as "not
// signed in" at the gate, and as a message naming the misrouting at the
// point where someone is actively trying to sign in — never as a JSON parse
// error in either place.
function htmlShell() {
  return new Response('<!doctype html><html><body><div id="root"></div></body></html>', {
    status: 200,
    headers: { 'Content-Type': 'text/html; charset=utf-8' },
  })
}

describe('an /auth/ request that never reached authd', () => {
  it('reads as not signed in rather than throwing a parse error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => htmlShell()))
    await expect(currentSession()).resolves.toBeUndefined()
    expect((globalThis as Record<string, unknown>).__FRAME_TOKEN__).toBeUndefined()
  })

  it('tells a signing-in user what is actually wrong', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => htmlShell()))
    await expect(loginWithPassword('a@b.c', 'pw')).rejects.toThrow(AuthUnreachableError)
    await expect(loginWithPassword('a@b.c', 'pw')).rejects.toThrow(/\/auth\//)
  })
})
