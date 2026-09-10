/**
 * The wire format of a pod shell.
 *
 * Kubernetes multiplexes stdin, stdout, stderr, an exit status and terminal
 * resizes over one WebSocket by prefixing every binary frame with a channel
 * byte. This module is that framing and the URL that opens it, and nothing
 * else — no DOM, no xterm, no WebSocket. That is deliberate: the terminal
 * component is `.tsx`, which no test in this repository executes, so
 * everything that could be wrong in a way a person would not notice lives
 * here instead.
 *
 * `v4.channel.k8s.io` is the protocol: binary frames (the `base64.channel...`
 * variants are the older, text ones) plus channel 3 carrying a `metav1.Status`
 * when the command ends, which is how an exit code reaches the browser at all.
 */

/** The multiplexing protocol offered to the apiserver. */
export const EXEC_SUBPROTOCOL = 'v4.channel.k8s.io'

/**
 * How a bearer token rides a WebSocket.
 *
 * `new WebSocket(url, protocols)` is the only WebSocket a browser can open and
 * it accepts no request headers, so the `Authorization` header every other
 * request in the console carries has nowhere to go. Kubernetes' own convention
 * puts the token in an extra subprotocol instead; here `frame-uiproxy` is what
 * consumes and strips it (`bearerProtocolPrefix` in
 * `internal/uiproxy/websocket.go`), because the token is authd's and the
 * apiserver would neither accept it nor recognise the entry.
 */
export const BEARER_SUBPROTOCOL_PREFIX = 'base64url.bearer.authorization.k8s.io.'

export const CHANNEL_STDIN = 0
export const CHANNEL_STDOUT = 1
export const CHANNEL_STDERR = 2
export const CHANNEL_ERROR = 3
export const CHANNEL_RESIZE = 4

export interface ExecTarget {
  namespace: string
  pod: string
  container: string
  /** argv, e.g. `['/bin/sh']` or `['/bin/sh', '-c', 'exec bash']`. */
  command: string[]
}

export interface ExecFrame {
  channel: number
  /**
   * The frame's bytes, channel byte removed. Deliberately not decoded: a UTF-8
   * sequence can be split across two WebSocket frames, and xterm's `write`
   * takes a Uint8Array and reassembles them. Decoding per frame here would
   * render a replacement character at the seam.
   */
  payload: Uint8Array
}

/**
 * The apiserver path that opens a shell.
 *
 * `stderr` is absent on purpose: with `tty=true` the apiserver's
 * PodExecOptions validation rejects it — a TTY merges the two streams — and
 * the request comes back 400 before anything opens.
 */
export function execPath(target: ExecTarget): string {
  const q = new URLSearchParams()
  q.set('container', target.container)
  q.set('stdin', 'true')
  q.set('stdout', 'true')
  q.set('tty', 'true')
  for (const c of target.command) q.append('command', c)
  return `/api/v1/namespaces/${target.namespace}/pods/${target.pod}/exec?${q.toString()}`
}

/** The same path as a WebSocket URL on the page's own origin. */
export function execUrl(target: ExecTarget, origin: string): string {
  return `${origin.replace(/^http/, 'ws')}${execPath(target)}`
}

/**
 * The token as a subprotocol entry.
 *
 * Unpadded base64url, because RFC 6455 forbids `=`, `+` and `/` in a
 * subprotocol token: a standard-alphabet or padded value makes `new
 * WebSocket()` throw a SyntaxError in the browser before a byte leaves the
 * tab, which leaves no request and no server-side trace to diagnose.
 *
 * `btoa` is safe here because a JWT is ASCII by construction; the two
 * character substitutions and the padding strip are what turn base64 into
 * base64url.
 */
export function bearerSubprotocol(token: string): string {
  const b64 = btoa(token).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return BEARER_SUBPROTOCOL_PREFIX + b64
}

/**
 * What to pass as `new WebSocket(url, protocols)`.
 *
 * The real protocol comes first: the server selects one entry and echoes it,
 * and it must be able to choose something the browser will accept.
 */
export function execSubprotocols(token: string): string[] {
  return [EXEC_SUBPROTOCOL, bearerSubprotocol(token)]
}

export function decodeFrame(data: ArrayBuffer): ExecFrame | undefined {
  const bytes = new Uint8Array(data)
  if (bytes.length === 0) return undefined
  return { channel: bytes[0], payload: bytes.subarray(1) }
}

/** A frame's payload as text — for the error channel, which carries JSON. */
export function frameText(frame: ExecFrame): string {
  return new TextDecoder().decode(frame.payload)
}

export function encodeStdin(text: string): Uint8Array {
  const body = new TextEncoder().encode(text)
  const out = new Uint8Array(body.length + 1)
  out[0] = CHANNEL_STDIN
  out.set(body, 1)
  return out
}

/**
 * A terminal resize.
 *
 * `Width` and `Height` are capitalised because the apiserver unmarshals this
 * into `remotecommand.TerminalSize`, a Go struct with no json tags. Lowercase
 * keys are accepted, ignored, and produce no error on either side: the pane
 * resizes in the browser, the pty does not, and every full-screen program in
 * the shell renders into the wrong width with nothing to look at.
 */
export function encodeResize(cols: number, rows: number): Uint8Array {
  const body = new TextEncoder().encode(JSON.stringify({ Width: cols, Height: rows }))
  const out = new Uint8Array(body.length + 1)
  out[0] = CHANNEL_RESIZE
  out.set(body, 1)
  return out
}

/**
 * What channel 3 has to say, or undefined when the command ended cleanly.
 *
 * This is the only place a refused or failed exec explains itself — a
 * `metav1.Status` with `status: "Success"` for a clean exit and a message
 * otherwise — so a body that is not JSON is returned as itself rather than
 * swallowed, which is the difference between a diagnosis and a blank pane.
 */
export function execExitMessage(json: string): string | undefined {
  try {
    const status = JSON.parse(json) as { status?: string; message?: string }
    if (status.status === 'Success') return undefined
    return status.message ?? 'the command ended with an error'
  } catch {
    return json
  }
}
