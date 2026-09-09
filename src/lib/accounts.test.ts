import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  acceptInvitation,
  inviteAccount,
  inviteTokenFromLocation,
  listCredentials,
  revokeCredential,
} from '@/lib/accounts'

/** Records what was fetched, so a test can assert on the request as well as the answer. */
function stubFetch(response: Response | ((input: string, init?: RequestInit) => Response)) {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string, init?: RequestInit) => {
      calls.push({ url: input, init })
      return typeof response === 'function' ? response(input, init) : response
    }),
  )
  return calls
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })

beforeEach(() => {
  vi.unstubAllGlobals()
})

describe('inviteTokenFromLocation', () => {
  it('finds the token on the invitation page', () => {
    expect(inviteTokenFromLocation('/invite', '?token=abc.def')).toBe('abc.def')
  })

  // Scoped to /invite on purpose: without the path check, any screen reached
  // with a stray ?token= in the URL would try to spend an invitation instead
  // of rendering.
  it('ignores a token anywhere but the invitation page', () => {
    expect(inviteTokenFromLocation('/', '?token=abc.def')).toBeUndefined()
    expect(inviteTokenFromLocation('/nodes', '?token=abc.def')).toBeUndefined()
  })

  it('is undefined when there is no token', () => {
    expect(inviteTokenFromLocation('/invite', '')).toBeUndefined()
    expect(inviteTokenFromLocation('/invite', '?token=')).toBeUndefined()
  })
})

describe('inviteAccount', () => {
  it('posts the address and role and returns the link', async () => {
    const calls = stubFetch(json({ url: 'https://frame.example/invite?token=sealed' }))
    const url = await inviteAccount('bob@example.com', 'viewer')
    expect(url).toBe('https://frame.example/invite?token=sealed')
    expect(calls[0].url).toBe('/auth/invite')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ email: 'bob@example.com', role: 'viewer' })
  })

  // authd's refusal names the way through — invite as operator or viewer,
  // then promote — and that sentence is the useful part, so it is surfaced
  // rather than replaced with a generic failure.
  it("surfaces authd's own message on a refusal", async () => {
    stubFetch(new Response('an invitation may only create an operator or a viewer', { status: 400 }))
    await expect(inviteAccount('bob@example.com', 'viewer')).rejects.toThrow(/operator or a viewer/)
  })

  // authd answers every route it owns with a body, even on failure — but a
  // proxy or an unrelated 5xx in front of it might not. The fallback message
  // must still name both what was being attempted and the status, rather
  // than surfacing an empty string.
  it('falls back to a status-bearing message when the response body is empty', async () => {
    stubFetch(new Response('', { status: 500 }))
    await expect(inviteAccount('bob@example.com', 'viewer')).rejects.toThrow(
      /could not create the invitation \(500\)/,
    )
  })
})

describe('acceptInvitation', () => {
  it('resolves on 204', async () => {
    const calls = stubFetch(new Response(null, { status: 204 }))
    await acceptInvitation('sealed')
    expect(calls[0].url).toBe('/auth/invite/accept')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ token: 'sealed' })
  })

  it('says the link is spent on 410, rather than repeating the status', async () => {
    stubFetch(new Response('this account already has a credential', { status: 410 }))
    await expect(acceptInvitation('sealed')).rejects.toThrow(/already has a credential/)
  })

  it('rejects on an expired or forged link', async () => {
    stubFetch(new Response('this invitation link is invalid or has expired', { status: 401 }))
    await expect(acceptInvitation('sealed')).rejects.toThrow(/invalid or has expired/)
  })
})

describe('listCredentials', () => {
  it("reads the caller's own keys", async () => {
    const calls = stubFetch(
      json({ credentials: [{ id: 'k1', label: 'YubiKey 5C', addedAt: '2026-09-09T10:00:00Z', signCount: 3 }] }),
    )
    const keys = await listCredentials()
    expect(keys).toHaveLength(1)
    expect(keys[0].label).toBe('YubiKey 5C')
    expect(calls[0].url).toBe('/auth/credentials')
  })

  it("escapes the address when reading someone else's", async () => {
    const calls = stubFetch(json({ credentials: [] }))
    await listCredentials('bob+test@example.com')
    expect(calls[0].url).toBe('/auth/credentials?user=bob%2Btest%40example.com')
  })

  it('answers an empty list for an account with no keys', async () => {
    stubFetch(json({ credentials: [] }))
    expect(await listCredentials()).toEqual([])
  })
})

describe('revokeCredential', () => {
  it('deletes by id, escaping it into the path', async () => {
    const calls = stubFetch(new Response(null, { status: 204 }))
    await revokeCredential('key/with+chars')
    expect(calls[0].url).toBe('/auth/credentials/key%2Fwith%2Bchars')
    expect(calls[0].init?.method).toBe('DELETE')
  })

  // The 409 is the design's own guard — the last key of a passkey-only
  // account — and it is the one refusal a user can act on, so it must not be
  // flattened into "request failed".
  it('surfaces the refusal to strand an account', async () => {
    stubFetch(
      new Response(
        'refusing to remove an account\'s last credential: root@example.com has no password sign-in',
        { status: 409 },
      ),
    )
    await expect(revokeCredential('k1')).rejects.toThrow(/last credential/)
  })
})
