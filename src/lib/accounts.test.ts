import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  acceptInvitation,
  canReissueInvitation,
  inviteAccount,
  inviteTokenFromLocation,
  isOnlyEnabledAdmin,
  listAccounts,
  listCredentials,
  reissueInvitation,
  revokeCredential,
  setAccountRole,
  setAccountState,
  type Account,
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

/**
 * `listAccounts`/`setAccountRole`/`setAccountState` go through
 * `createFrameClient().users`, which reads `window.__FRAME_NAMESPACE__` and
 * `window.__FRAME_TOKEN__` (see `frame-sdk.ts`). `window` doesn't exist under
 * vitest's `environment: 'node'`, so it must be stubbed before any of those
 * three run — matching `stubBrowser()` in `frame-sdk.test.ts`.
 */
function stubBrowser(overrides: Record<string, string> = {}) {
  vi.stubGlobal('window', overrides)
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })

/**
 * The exact path every FrameUser read and write must take.
 *
 * Spelled out in full, namespace segment included, rather than matched on
 * `.includes('/frameusers')`. The substring form is what let a whole branch
 * ship with the Accounts screen pointed at the wrong namespace: FrameUsers
 * exist only where authd runs (`cluster-control` — its Role is namespaced
 * there, so it cannot create them anywhere else), while the SDK was building
 * the path from `config().frameNamespace`, whose default is `default` and
 * which nothing in the repo or the docs ever sets. Every test still passed,
 * because `/apis/.../namespaces/default/frameusers` contains `/frameusers`
 * just as happily as the correct path does. On a real cluster the list came
 * back empty with no error and both PATCHes 404'd.
 */
const FRAMEUSERS_PATH = '/apis/frame.plume-labs.io/v1beta1/namespaces/cluster-control/frameusers'

beforeEach(() => {
  vi.unstubAllGlobals()
})

describe('inviteTokenFromLocation', () => {
  it('finds the token in the fragment on the invitation page', () => {
    expect(inviteTokenFromLocation('/invite', '#token=abc.def')).toBe('abc.def')
  })

  // The fragment is the point, not an implementation detail: it is never sent
  // to a server, so the token stays out of access logs and out of the
  // same-origin Referer of every asset /invite loads. Reading the query
  // string would put a live bearer credential back into all of them, and
  // nothing else in the suite would notice.
  it('does not read a token from the query string', () => {
    expect(inviteTokenFromLocation('/invite', '?token=abc.def')).toBeUndefined()
  })

  // Scoped to /invite on purpose: without the path check, any screen reached
  // with a stray #token= in the URL would try to spend an invitation instead
  // of rendering.
  it('ignores a token anywhere but the invitation page', () => {
    expect(inviteTokenFromLocation('/', '#token=abc.def')).toBeUndefined()
    expect(inviteTokenFromLocation('/nodes', '#token=abc.def')).toBeUndefined()
  })

  it('is undefined when there is no token', () => {
    expect(inviteTokenFromLocation('/invite', '')).toBeUndefined()
    expect(inviteTokenFromLocation('/invite', '#token=')).toBeUndefined()
  })
})

describe('inviteAccount', () => {
  it('posts the address and role and returns the link', async () => {
    const calls = stubFetch(json({ url: 'https://frame.example/invite#token=sealed' }))
    const url = await inviteAccount('bob@example.com', 'viewer')
    expect(url).toBe('https://frame.example/invite#token=sealed')
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

describe('reissueInvitation', () => {
  it('posts only the address and returns the link', async () => {
    const calls = stubFetch(json({ url: 'https://frame.example/invite#token=sealed2' }))
    const url = await reissueInvitation('bob@example.com')
    expect(url).toBe('https://frame.example/invite#token=sealed2')
    expect(calls[0].url).toBe('/auth/invite/link')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ email: 'bob@example.com' })
  })

  it("surfaces authd's own message when the account is unknown", async () => {
    stubFetch(new Response('no account with that email', { status: 404 }))
    await expect(reissueInvitation('ghost@example.com')).rejects.toThrow(/no account with that email/)
  })

  // 410: the account already enrolled a credential, so a link for it would
  // be refused at acceptance anyway — authd's wording is the useful part.
  it("surfaces authd's own message when the account already has a credential", async () => {
    stubFetch(new Response('this account already has a credential', { status: 410 }))
    await expect(reissueInvitation('bob@example.com')).rejects.toThrow(/already has a credential/)
  })

  // 403: a disabled account cannot be issued an identity.
  it("surfaces authd's own message when the account is disabled", async () => {
    stubFetch(new Response('this account is disabled', { status: 403 }))
    await expect(reissueInvitation('bob@example.com')).rejects.toThrow(/this account is disabled/)
  })

  it('falls back to a status-bearing message when the response body is empty', async () => {
    stubFetch(new Response('', { status: 500 }))
    await expect(reissueInvitation('bob@example.com')).rejects.toThrow(
      /could not create the invitation link \(500\)/,
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

describe('listAccounts', () => {
  it('reshapes the FrameUser list, defaults an unset state to enabled, and reads each key count', async () => {
    stubBrowser()
    const urls: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string) => {
        const url = String(input)
        urls.push(url)
        if (url.includes('/frameusers')) {
          return new Response(
            JSON.stringify({
              items: [
                { metadata: { name: 'alice' }, spec: { email: 'alice@example.com', role: 'admin', state: 'enabled' } },
                // No state at all — the apiserver default, or a CR written before the field existed.
                { metadata: { name: 'bob' }, spec: { email: 'bob@example.com', role: 'viewer' } },
              ],
            }),
            { status: 200, headers: { 'content-type': 'application/json' } },
          )
        }
        if (url.includes('user=bob')) {
          return new Response(JSON.stringify({ credentials: [] }), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          })
        }
        return new Response(
          JSON.stringify({ credentials: [{ id: 'k1', label: 'YubiKey', addedAt: 't', signCount: 1 }] }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        )
      }),
    )

    expect(await listAccounts()).toEqual([
      { name: 'alice', email: 'alice@example.com', role: 'admin', state: 'enabled', keyCount: 1 },
      { name: 'bob', email: 'bob@example.com', role: 'viewer', state: 'enabled', keyCount: 0 },
    ])
    // The list is read from authd's namespace, not the configurable one.
    expect(urls[0]).toBe(FRAMEUSERS_PATH)
  })

  // The account list must not render "0 keys" for an account whose key count
  // it actually failed to read — that is a lie, not an unknown, and the two
  // must stay distinguishable in what this function returns.
  it('reports -1, not 0, when a key count could not be read', async () => {
    stubBrowser()
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string) => {
        const url = String(input)
        if (url.includes('/frameusers')) {
          return new Response(
            JSON.stringify({
              items: [{ metadata: { name: 'carol' }, spec: { email: 'carol@example.com', role: 'viewer' } }],
            }),
            { status: 200, headers: { 'content-type': 'application/json' } },
          )
        }
        return new Response('authd unreachable', { status: 502 })
      }),
    )

    const [account] = await listAccounts()
    expect(account.keyCount).toBe(-1)
  })
})

describe('setAccountRole', () => {
  it('PATCHes spec.role and labels the action for the audit trail', async () => {
    stubBrowser()
    const calls = stubFetch(new Response(null, { status: 204 }))
    await setAccountRole('bob', 'bob@example.com', 'admin')
    expect(calls[0].url).toBe(`${FRAMEUSERS_PATH}/bob`)
    expect(calls[0].init?.method).toBe('PATCH')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ spec: { role: 'admin' } })
    // The action names what changed, not the mechanics of the request — a
    // promotion and a demotion must not collapse into the same Tasks-screen
    // entry ("patch frameusers/bob") the way an unlabelled PATCH would.
    expect((calls[0].init?.headers as Record<string, string>)['X-Frame-Action']).toBe('set bob@example.com to admin')
  })
})

describe('setAccountState', () => {
  it('labels a disable distinctly from an enable', async () => {
    stubBrowser()
    const disableCalls = stubFetch(new Response(null, { status: 204 }))
    await setAccountState('bob', 'bob@example.com', 'disabled')
    expect(disableCalls[0].url).toBe(`${FRAMEUSERS_PATH}/bob`)
    expect(JSON.parse(String(disableCalls[0].init?.body))).toEqual({ spec: { state: 'disabled' } })
    expect((disableCalls[0].init?.headers as Record<string, string>)['X-Frame-Action']).toBe(
      'disable bob@example.com',
    )

    const enableCalls = stubFetch(new Response(null, { status: 204 }))
    await setAccountState('bob', 'bob@example.com', 'enabled')
    expect((enableCalls[0].init?.headers as Record<string, string>)['X-Frame-Action']).toBe(
      'enable bob@example.com',
    )
  })
})

describe('isOnlyEnabledAdmin', () => {
  const account = (over: Partial<Account>): Account => ({
    name: over.email ?? 'x',
    email: 'x@example.com',
    role: 'viewer',
    state: 'enabled',
    keyCount: 0,
    ...over,
  })

  it('is true for the sole enabled admin among other accounts', () => {
    const accounts = [
      account({ email: 'admin@example.com', role: 'admin' }),
      account({ email: 'viewer@example.com', role: 'viewer' }),
    ]
    expect(isOnlyEnabledAdmin(accounts, 'admin@example.com')).toBe(true)
  })

  it('is false when another enabled admin exists', () => {
    const accounts = [
      account({ email: 'a@example.com', role: 'admin' }),
      account({ email: 'b@example.com', role: 'admin' }),
    ]
    expect(isOnlyEnabledAdmin(accounts, 'a@example.com')).toBe(false)
  })

  // Matches requireAnotherAdmin in frameuser_webhook.go, not anyAdminExists:
  // a disabled admin cannot sign in to authorize anything, so they are not a
  // backup and must not make this false.
  it('does not count a disabled admin as a backup', () => {
    const accounts = [
      account({ email: 'a@example.com', role: 'admin', state: 'enabled' }),
      account({ email: 'b@example.com', role: 'admin', state: 'disabled' }),
    ]
    expect(isOnlyEnabledAdmin(accounts, 'a@example.com')).toBe(true)
  })

  it('is false for an account that is not an admin at all', () => {
    const accounts = [account({ email: 'a@example.com', role: 'viewer' })]
    expect(isOnlyEnabledAdmin(accounts, 'a@example.com')).toBe(false)
  })
})

describe('canReissueInvitation', () => {
  const account = (over: Partial<Account>): Account => ({
    name: 'bob',
    email: 'bob@example.com',
    role: 'viewer',
    state: 'enabled',
    keyCount: 0,
    ...over,
  })

  it('is true for an enabled account holding no key', () => {
    expect(canReissueInvitation(account({ keyCount: 0 }))).toBe(true)
  })

  // -1 means the key read failed, not "holds no key" — the same distinction
  // `keyCount`'s own doc comment draws. A `<= 0` predicate would re-admit
  // this and offer a link for an account that may already be enrolled.
  it('is false when the key count failed to load', () => {
    expect(canReissueInvitation(account({ keyCount: -1 }))).toBe(false)
  })

  it('is false for an account already holding a key', () => {
    expect(canReissueInvitation(account({ keyCount: 1 }))).toBe(false)
  })

  it('is false for a disabled account, even with no key', () => {
    expect(canReissueInvitation(account({ keyCount: 0, state: 'disabled' }))).toBe(false)
  })
})
