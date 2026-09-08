import { describe, expect, it } from 'vitest'
import {
  authenticationResponseToJSON,
  base64UrlToBuffer,
  bufferToBase64Url,
  registrationResponseToJSON,
  toCredentialCreationOptions,
  toCredentialRequestOptions,
  type CredentialCreationOptionsJSON,
  type CredentialRequestOptionsJSON,
} from './webauthn'

function bytesOf(...values: number[]): Uint8Array {
  return new Uint8Array(values)
}

function toArray(buffer: ArrayBuffer): number[] {
  return Array.from(new Uint8Array(buffer))
}

describe('base64UrlToBuffer / bufferToBase64Url', () => {
  it('round-trips an empty buffer to an empty string', () => {
    const encoded = bufferToBase64Url(new ArrayBuffer(0))
    expect(encoded).toBe('')
    expect(toArray(base64UrlToBuffer(encoded))).toEqual([])
  })

  // Base64 padding only ever needs 0, 1, or 2 '=' depending on byteLength % 3
  // (never 1 leftover base64 char). Round-tripping lengths 1..9 walks through
  // every remainder at least three times, which is where an off-by-one in
  // the padding math shows up — "works for 7 of 8 keys" is exactly a modulo
  // bug like this.
  it('round-trips every byte length from 1 to 24, both directions', () => {
    for (let len = 1; len <= 24; len++) {
      const original = bytesOf(...Array.from({ length: len }, (_, i) => (i * 37 + len * 7) % 256))
      const encoded = bufferToBase64Url(original.buffer)
      expect(encoded).not.toContain('=')
      expect(toArray(base64UrlToBuffer(encoded))).toEqual(Array.from(original))
    }
  })

  it('emits the URL-safe alphabet, never "+", "/" or "="', () => {
    // Chosen so the standard base64 alphabet would need both '+' and '/'.
    const original = bytesOf(0xfb, 0xff, 0xbf, 0xff)
    const encoded = bufferToBase64Url(original.buffer)
    expect(encoded).not.toMatch(/[+/=]/)
    expect(encoded).toMatch(/[-_]/)
  })

  it('decodes "-" and "_" to the same bytes "+" and "/" would carry in standard base64', () => {
    // bytes [0xfb, 0xff] standard-encode to "+/8=" (padded); "-_8" is the
    // base64url form of exactly the same bits.
    expect(toArray(base64UrlToBuffer('-_8'))).toEqual([0xfb, 0xff])
  })

  it('decodes a string with no padding even where standard base64 would need "=="', () => {
    // 1 byte needs 2 '=' in standard base64; authd never sends padding.
    expect(toArray(base64UrlToBuffer('_w'))).toEqual([0xff])
  })

  it('decodes a string with no padding even where standard base64 would need "="', () => {
    // 2 bytes need 1 '=' in standard base64.
    expect(toArray(base64UrlToBuffer('-_8'))).toEqual([0xfb, 0xff])
  })

  it('tolerates padding if present, so a standards-strict caller is not punished', () => {
    expect(toArray(base64UrlToBuffer('_w=='))).toEqual([0xff])
  })

  it('encodes an ArrayBufferView (typed-array slice), not just a fresh ArrayBuffer', () => {
    const backing = bytesOf(0, 1, 2, 3, 4, 5)
    const view = backing.subarray(1, 4) // offset 1, length 3, shares the backing buffer
    const encoded = bufferToBase64Url(view)
    expect(toArray(base64UrlToBuffer(encoded))).toEqual([1, 2, 3])
  })
})

describe('toCredentialCreationOptions', () => {
  const json: CredentialCreationOptionsJSON = {
    publicKey: {
      rp: { id: 'example.test', name: 'Frame Cluster Control' },
      user: { id: 'dXNlci1pZA', name: 'admin@example.test', displayName: 'admin@example.test' },
      challenge: '_w', // [0xff]
      pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
      timeout: 60000,
      excludeCredentials: [{ type: 'public-key', id: '-_8', transports: ['internal'] }],
      authenticatorSelection: { residentKey: 'required', userVerification: 'preferred' },
      attestation: 'none',
      extensions: { credProps: true },
    },
  }

  it('base64url-decodes challenge, user.id, and excludeCredentials[].id into ArrayBuffers', () => {
    const opts = toCredentialCreationOptions(json)
    expect(toArray(opts.challenge as ArrayBuffer)).toEqual([0xff])
    expect(toArray(opts.user.id as ArrayBuffer)).toEqual(toArray(base64UrlToBuffer('dXNlci1pZA')))
    expect(opts.excludeCredentials).toHaveLength(1)
    expect(toArray(opts.excludeCredentials![0].id as ArrayBuffer)).toEqual([0xfb, 0xff])
  })

  it('passes rp, user.name/displayName, pubKeyCredParams, authenticatorSelection, attestation and extensions through unchanged', () => {
    const opts = toCredentialCreationOptions(json)
    expect(opts.rp).toEqual({ id: 'example.test', name: 'Frame Cluster Control' })
    expect(opts.user.name).toBe('admin@example.test')
    expect(opts.user.displayName).toBe('admin@example.test')
    expect(opts.pubKeyCredParams).toEqual([{ type: 'public-key', alg: -7 }])
    expect(opts.authenticatorSelection).toEqual({ residentKey: 'required', userVerification: 'preferred' })
    expect(opts.attestation).toBe('none')
    expect(opts.extensions).toEqual({ credProps: true })
    expect(opts.timeout).toBe(60000)
  })

  it('omits excludeCredentials when authd sent none (first enrolment, nothing to exclude)', () => {
    const opts = toCredentialCreationOptions({
      publicKey: { ...json.publicKey, excludeCredentials: undefined },
    })
    expect(opts.excludeCredentials).toBeUndefined()
  })
})

describe('toCredentialRequestOptions', () => {
  const json: CredentialRequestOptionsJSON = {
    publicKey: {
      challenge: '_w',
      timeout: 60000,
      rpId: 'example.test',
      userVerification: 'preferred',
    },
  }

  it('base64url-decodes challenge into an ArrayBuffer', () => {
    const opts = toCredentialRequestOptions(json)
    expect(toArray(opts.challenge as ArrayBuffer)).toEqual([0xff])
  })

  it('leaves allowCredentials undefined for the usernameless (discoverable) ceremony', () => {
    const opts = toCredentialRequestOptions(json)
    expect(opts.allowCredentials).toBeUndefined()
  })

  it('passes rpId and userVerification through unchanged', () => {
    const opts = toCredentialRequestOptions(json)
    expect(opts.rpId).toBe('example.test')
    expect(opts.userVerification).toBe('preferred')
  })

  it('decodes allowCredentials[].id when present (non-discoverable path, still supported)', () => {
    const opts = toCredentialRequestOptions({
      publicKey: { ...json.publicKey, allowCredentials: [{ type: 'public-key', id: '-_8' }] },
    })
    expect(opts.allowCredentials).toHaveLength(1)
    expect(toArray(opts.allowCredentials![0].id as ArrayBuffer)).toEqual([0xfb, 0xff])
  })
})

/**
 * `getAuthenticatorData`/`getPublicKey`/`getPublicKeyAlgorithm`/`getTransports`
 * are declared unconditionally by `lib.dom.d.ts` but are WebAuthn L3
 * additions — build a mock that can omit them, to prove the shaping function
 * degrades gracefully rather than throwing on an older browser.
 */
function mockAttestationCredential(opts: { withL3Methods: boolean; authenticatorAttachment?: string | null }) {
  const response: Record<string, unknown> = {
    clientDataJSON: bytesOf(1, 2, 3).buffer,
    attestationObject: bytesOf(4, 5, 6, 7).buffer,
  }
  if (opts.withL3Methods) {
    response.getAuthenticatorData = () => bytesOf(9, 9).buffer
    response.getPublicKey = () => bytesOf(8, 8, 8).buffer
    response.getPublicKeyAlgorithm = () => -7
    response.getTransports = () => ['internal']
  }
  // `?? 'platform'` would be wrong here: it collapses an explicitly-passed
  // `null` (the case under test) into the default, since `null` is one of
  // `??`'s trigger values. Only fall back when the caller omitted the key.
  const authenticatorAttachment = Object.prototype.hasOwnProperty.call(opts, 'authenticatorAttachment')
    ? opts.authenticatorAttachment
    : 'platform'
  return {
    id: 'AQIDBA', // base64url, arbitrary
    type: 'public-key',
    rawId: bytesOf(10, 20, 30).buffer,
    authenticatorAttachment,
    response,
    getClientExtensionResults: () => ({ credProps: { rk: true } }),
  } as unknown as PublicKeyCredential
}

describe('registrationResponseToJSON', () => {
  it('shapes id, type, rawId, clientExtensionResults and the required response fields', () => {
    const cred = mockAttestationCredential({ withL3Methods: false })
    const json = registrationResponseToJSON(cred)
    expect(json.id).toBe('AQIDBA')
    expect(json.type).toBe('public-key')
    expect(toArray(base64UrlToBuffer(json.rawId))).toEqual([10, 20, 30])
    expect(json.clientExtensionResults).toEqual({ credProps: { rk: true } })
    expect(toArray(base64UrlToBuffer(json.response.clientDataJSON))).toEqual([1, 2, 3])
    expect(toArray(base64UrlToBuffer(json.response.attestationObject))).toEqual([4, 5, 6, 7])
  })

  it('omits the L3 fields rather than throwing when the browser does not expose them', () => {
    const cred = mockAttestationCredential({ withL3Methods: false })
    const json = registrationResponseToJSON(cred)
    expect(json.response.authenticatorData).toBeUndefined()
    expect(json.response.publicKey).toBeUndefined()
    expect(json.response.publicKeyAlgorithm).toBeUndefined()
    expect(json.response.transports).toBeUndefined()
  })

  it('includes the L3 fields, base64url-encoded, when the browser exposes them', () => {
    const cred = mockAttestationCredential({ withL3Methods: true })
    const json = registrationResponseToJSON(cred)
    expect(toArray(base64UrlToBuffer(json.response.authenticatorData!))).toEqual([9, 9])
    expect(toArray(base64UrlToBuffer(json.response.publicKey!))).toEqual([8, 8, 8])
    expect(json.response.publicKeyAlgorithm).toBe(-7)
    expect(json.response.transports).toEqual(['internal'])
  })

  it('maps a null authenticatorAttachment to undefined rather than sending the literal null', () => {
    const cred = mockAttestationCredential({ withL3Methods: false, authenticatorAttachment: null })
    const json = registrationResponseToJSON(cred)
    expect(json.authenticatorAttachment).toBeUndefined()
  })
})

function mockAssertionCredential(opts: { userHandle: ArrayBuffer | null }) {
  const response = {
    clientDataJSON: bytesOf(1, 1, 1).buffer,
    authenticatorData: bytesOf(2, 2, 2, 2).buffer,
    signature: bytesOf(3, 3).buffer,
    userHandle: opts.userHandle,
  }
  return {
    id: 'AQIDBA',
    type: 'public-key',
    rawId: bytesOf(5, 6, 7).buffer,
    authenticatorAttachment: 'cross-platform',
    response,
    getClientExtensionResults: () => ({}),
  } as unknown as PublicKeyCredential
}

describe('authenticationResponseToJSON', () => {
  it('shapes id, type, rawId and the required response fields', () => {
    const cred = mockAssertionCredential({ userHandle: bytesOf(9, 9).buffer })
    const json = authenticationResponseToJSON(cred)
    expect(json.id).toBe('AQIDBA')
    expect(json.type).toBe('public-key')
    expect(toArray(base64UrlToBuffer(json.rawId))).toEqual([5, 6, 7])
    expect(toArray(base64UrlToBuffer(json.response.clientDataJSON))).toEqual([1, 1, 1])
    expect(toArray(base64UrlToBuffer(json.response.authenticatorData))).toEqual([2, 2, 2, 2])
    expect(toArray(base64UrlToBuffer(json.response.signature))).toEqual([3, 3])
  })

  // This is the field that matters most: BeginDiscoverableLogin's usernameless
  // ceremony means go-webauthn identifies the account purely from
  // response.userHandle. Dropping it silently would turn every login attempt
  // into a 401 that looks identical to a wrong credential.
  it('includes userHandle, base64url-encoded, when the authenticator returns one', () => {
    const cred = mockAssertionCredential({ userHandle: bytesOf(0xfb, 0xff).buffer })
    const json = authenticationResponseToJSON(cred)
    expect(json.response.userHandle).toBeDefined()
    expect(toArray(base64UrlToBuffer(json.response.userHandle!))).toEqual([0xfb, 0xff])
  })

  it('omits userHandle when the authenticator returns null', () => {
    const cred = mockAssertionCredential({ userHandle: null })
    const json = authenticationResponseToJSON(cred)
    expect(json.response.userHandle).toBeUndefined()
  })

  it('omits userHandle when the authenticator returns a zero-length buffer', () => {
    const cred = mockAssertionCredential({ userHandle: new ArrayBuffer(0) })
    const json = authenticationResponseToJSON(cred)
    expect(json.response.userHandle).toBeUndefined()
  })
})
