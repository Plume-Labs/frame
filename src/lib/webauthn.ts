/**
 * Data-shape translation for the WebAuthn ceremonies against authd.
 *
 * authd speaks the go-webauthn library's JSON encoding directly (see
 * `internal/authd/webauthn.go` and `internal/authd/server_webauthn.go`):
 * every binary field — challenges, credential/user ids, signatures,
 * attestation/assertion blobs — is `protocol.URLEncodedBase64`, which is
 * `encoding/base64.RawURLEncoding` (base64url, no padding). The browser's
 * `navigator.credentials.create()`/`.get()` want those same fields as
 * `ArrayBuffer`s, and hand back `ArrayBuffer`s in the credential they
 * produce.
 *
 * This module is that translation layer in both directions, and nothing
 * else — it never touches `navigator.credentials` itself, so it can be
 * exercised under plain node in `webauthn.test.ts`. The two React call
 * sites (`auth.ts` for the ceremony, the enrolment UI for the label) stay
 * thin wrappers around these functions.
 */

// --- base64url <-> ArrayBuffer ---------------------------------------------

/**
 * Decode a base64url string (RFC 4648 §5, no padding — exactly what
 * `protocol.URLEncodedBase64.MarshalJSON` produces) into an `ArrayBuffer`.
 *
 * `atob` only understands the standard alphabet and requires padding, so
 * both are restored before decoding. An empty string decodes to a
 * zero-length buffer rather than throwing, mirroring how the Go type treats
 * `""`/`null`.
 */
export function base64UrlToBuffer(value: string): ArrayBuffer {
  const standard = value.replace(/-/g, '+').replace(/_/g, '/')
  const remainder = standard.length % 4
  const padded = remainder === 0 ? standard : standard + '='.repeat(4 - remainder)
  const binary = padded === '' ? '' : atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i)
  }
  return bytes.buffer
}

/**
 * Encode an `ArrayBuffer` (or a typed view over one) as base64url, no
 * padding — the encoding `protocol.URLEncodedBase64.MarshalJSON` expects
 * back from the client.
 */
export function bufferToBase64Url(source: ArrayBuffer | ArrayBufferView): string {
  const bytes = ArrayBuffer.isView(source)
    ? new Uint8Array(source.buffer, source.byteOffset, source.byteLength)
    : new Uint8Array(source)
  let binary = ''
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i])
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// --- server options JSON -> browser options --------------------------------

/**
 * The body `POST /auth/register/begin` returns: `protocol.CredentialCreation`
 * (`internal/authd/webauthn.go` `BeginRegistration` -> `seal` -> `json.Marshal`),
 * i.e. `{"publicKey": PublicKeyCredentialCreationOptions, "mediation"?: ...}`.
 * Every id-shaped field is base64url per `URLEncodedBase64`; everything else
 * (rp, authenticatorSelection, attestation, extensions, hints) is passed
 * through by the shaping function untouched, since the browser wants those
 * verbatim too.
 */
export interface CredentialCreationOptionsJSON {
  publicKey: {
    rp: { id: string; name: string }
    user: { id: string; name: string; displayName: string }
    challenge: string
    pubKeyCredParams: Array<{ type: 'public-key'; alg: number }>
    timeout?: number
    excludeCredentials?: Array<{ type: 'public-key'; id: string; transports?: string[] }>
    authenticatorSelection?: {
      authenticatorAttachment?: string
      requireResidentKey?: boolean
      residentKey?: string
      userVerification?: string
    }
    attestation?: string
    attestationFormats?: string[]
    hints?: string[]
    extensions?: Record<string, unknown>
  }
  mediation?: string
}

/**
 * The body `POST /auth/login/begin` returns: `protocol.CredentialAssertion`
 * from `BeginDiscoverableLogin`. Usernameless, so `allowCredentials` is
 * absent (an empty slice marshals as omitted, per `omitempty`).
 */
export interface CredentialRequestOptionsJSON {
  publicKey: {
    challenge: string
    timeout?: number
    rpId?: string
    allowCredentials?: Array<{ type: 'public-key'; id: string; transports?: string[] }>
    userVerification?: string
    hints?: string[]
    extensions?: Record<string, unknown>
  }
  mediation?: string
}

/** Shape the `/auth/register/begin` response into `navigator.credentials.create()`'s input. */
export function toCredentialCreationOptions(
  json: CredentialCreationOptionsJSON,
): PublicKeyCredentialCreationOptions {
  const { publicKey } = json
  return {
    rp: publicKey.rp,
    user: {
      id: base64UrlToBuffer(publicKey.user.id),
      name: publicKey.user.name,
      displayName: publicKey.user.displayName,
    },
    challenge: base64UrlToBuffer(publicKey.challenge),
    pubKeyCredParams: publicKey.pubKeyCredParams,
    timeout: publicKey.timeout,
    excludeCredentials: publicKey.excludeCredentials?.map((cred) => ({
      type: cred.type,
      id: base64UrlToBuffer(cred.id),
      transports: cred.transports as AuthenticatorTransport[] | undefined,
    })),
    authenticatorSelection: publicKey.authenticatorSelection as
      | AuthenticatorSelectionCriteria
      | undefined,
    attestation: publicKey.attestation as AttestationConveyancePreference | undefined,
    extensions: publicKey.extensions,
  }
}

/** Shape the `/auth/login/begin` response into `navigator.credentials.get()`'s input. */
export function toCredentialRequestOptions(
  json: CredentialRequestOptionsJSON,
): PublicKeyCredentialRequestOptions {
  const { publicKey } = json
  return {
    challenge: base64UrlToBuffer(publicKey.challenge),
    timeout: publicKey.timeout,
    rpId: publicKey.rpId,
    allowCredentials: publicKey.allowCredentials?.map((cred) => ({
      type: cred.type,
      id: base64UrlToBuffer(cred.id),
      transports: cred.transports as AuthenticatorTransport[] | undefined,
    })),
    userVerification: publicKey.userVerification as UserVerificationRequirement | undefined,
    extensions: publicKey.extensions,
  }
}

// --- browser credential -> server JSON -------------------------------------

/**
 * The body `POST /auth/register/finish` expects: `protocol.CredentialCreationResponse`
 * (`protocol.ParseCredentialCreationResponseBody` in `FinishRegistration`).
 * `Parse()` (attestation.go) only reads `id`/`type` (for its own validation),
 * `response.clientDataJSON`, `response.attestationObject`, and
 * `response.transports` — `response.authenticatorData`/`publicKey`/
 * `publicKeyAlgorithm` are informational L3 fields the parser never
 * dereferences, so they are sent when the browser exposes them and omitted
 * otherwise rather than being treated as required.
 */
export interface RegistrationResponseJSON {
  id: string
  type: string
  rawId: string
  authenticatorAttachment?: string
  clientExtensionResults: Record<string, unknown>
  response: {
    clientDataJSON: string
    attestationObject: string
    authenticatorData?: string
    transports?: string[]
    publicKey?: string
    publicKeyAlgorithm?: number
  }
}

/**
 * The body `POST /auth/login/finish` expects: `protocol.CredentialAssertionResponse`
 * (`protocol.ParseCredentialRequestResponseBody` in `FinishLogin`).
 * `response.userHandle` is not cosmetic here: `BeginDiscoverableLogin` runs a
 * usernameless ceremony, and go-webauthn's `ValidateDiscoverableLogin`
 * rejects the assertion outright ("Client-side Discoverable Assertion was
 * attempted with a blank User Handle") if it is missing — it is how the
 * library's lookup callback (`a.store.ByCredentialID`) gets a user at all.
 * It is only omitted here when the authenticator itself returned nothing.
 */
export interface AuthenticationResponseJSON {
  id: string
  type: string
  rawId: string
  authenticatorAttachment?: string
  clientExtensionResults: Record<string, unknown>
  response: {
    clientDataJSON: string
    authenticatorData: string
    signature: string
    userHandle?: string
  }
}

/**
 * Some `AuthenticatorAttestationResponse`/`AuthenticatorAssertionResponse`
 * accessors (`getPublicKey`, `getAuthenticatorData`, ...) are WebAuthn L3
 * additions. `lib.dom.d.ts` declares them unconditionally, but an older
 * browser's runtime object may not actually have them — call defensively
 * rather than trusting the type.
 */
function callIfPresent<T>(fn: (() => T) | undefined): T | undefined {
  return typeof fn === 'function' ? fn() : undefined
}

/** Shape a completed `navigator.credentials.create()` result for `/auth/register/finish`. */
export function registrationResponseToJSON(credential: PublicKeyCredential): RegistrationResponseJSON {
  const response = credential.response as AuthenticatorAttestationResponse
  const authenticatorData = callIfPresent(response.getAuthenticatorData?.bind(response))
  const publicKey = callIfPresent(response.getPublicKey?.bind(response))
  const publicKeyAlgorithm = callIfPresent(response.getPublicKeyAlgorithm?.bind(response))
  const transports = callIfPresent(response.getTransports?.bind(response))

  return {
    id: credential.id,
    type: credential.type,
    rawId: bufferToBase64Url(credential.rawId),
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: credential.getClientExtensionResults() as Record<string, unknown>,
    response: {
      clientDataJSON: bufferToBase64Url(response.clientDataJSON),
      attestationObject: bufferToBase64Url(response.attestationObject),
      authenticatorData: authenticatorData ? bufferToBase64Url(authenticatorData) : undefined,
      transports,
      publicKey: publicKey ? bufferToBase64Url(publicKey) : undefined,
      publicKeyAlgorithm,
    },
  }
}

/** Shape a completed `navigator.credentials.get()` result for `/auth/login/finish`. */
export function authenticationResponseToJSON(credential: PublicKeyCredential): AuthenticationResponseJSON {
  const response = credential.response as AuthenticatorAssertionResponse
  const userHandle = response.userHandle && response.userHandle.byteLength > 0 ? response.userHandle : undefined

  return {
    id: credential.id,
    type: credential.type,
    rawId: bufferToBase64Url(credential.rawId),
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: credential.getClientExtensionResults() as Record<string, unknown>,
    response: {
      clientDataJSON: bufferToBase64Url(response.clientDataJSON),
      authenticatorData: bufferToBase64Url(response.authenticatorData),
      signature: bufferToBase64Url(response.signature),
      userHandle: userHandle ? bufferToBase64Url(userHandle) : undefined,
    },
  }
}
