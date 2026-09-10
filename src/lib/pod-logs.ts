/**
 * Reading a pod's logs.
 *
 * `GET pods/<name>/log` is an ordinary HTTP response — chunked when
 * `follow=true` — not a WebSocket, so this needs no protocol, only a path and
 * a reader that copes with chunk boundaries falling anywhere.
 *
 * Worth stating where it will be read: **a log read leaves no FrameTask.** The
 * recorder ignores reads by design and this is a read, so there is no record
 * that anyone read one — and a log is a likely place for a credential to
 * appear in plain text. The mitigation is the RBAC tier (`pods/log` starts at
 * operator, `deploy/kubernetes/base/rbac.yaml`), not the audit trail.
 */

export interface PodLogQuery {
  namespace: string
  pod: string
  container: string
  /** Hold the connection open and stream new lines as the container writes them. */
  follow?: boolean
  /** Read the previous, terminated instance of the container instead. */
  previous?: boolean
  tailLines?: number
}

/**
 * The apiserver path for one container's logs.
 *
 * `follow` and `previous` are mutually exclusive and `previous` wins: the dead
 * instance will never write another byte, and the apiserver answers 400 for
 * the pair — which would make the previous-logs tab look broken for anyone who
 * had left follow switched on.
 */
export function podLogPath(q: PodLogQuery): string {
  const params = new URLSearchParams()
  params.set('container', q.container)
  if (q.previous) params.set('previous', 'true')
  else if (q.follow) params.set('follow', 'true')
  if (q.tailLines !== undefined) params.set('tailLines', String(q.tailLines))
  return `/api/v1/namespaces/${q.namespace}/pods/${q.pod}/log?${params.toString()}`
}

/**
 * The one method this needs from a stream. `response.body.getReader()`
 * satisfies it, and so does a two-line fake — which is why it is spelled out
 * rather than typed as a `ReadableStreamDefaultReader`.
 */
export interface ByteReader {
  read(): Promise<{ done: boolean; value?: Uint8Array }>
}

/**
 * Call `onLine` once per line, whatever the chunking.
 *
 * Two things it must do that a naive loop does not: keep a partial line across
 * reads (a chunk boundary lands mid-line constantly), and decode with
 * `{ stream: true }` so a UTF-8 character split across two chunks is
 * reassembled rather than turned into two replacement characters. The trailing
 * flush emits the last line of a stream that ended without a newline — which
 * on a live container is the line someone is waiting for.
 */
export async function pumpLogLines(
  reader: ByteReader,
  onLine: (line: string) => void,
): Promise<void> {
  const decoder = new TextDecoder()
  let buffer = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (value && value.length > 0) {
      buffer += decoder.decode(value, { stream: true })
      for (let nl = buffer.indexOf('\n'); nl !== -1; nl = buffer.indexOf('\n')) {
        onLine(buffer.slice(0, nl))
        buffer = buffer.slice(nl + 1)
      }
    }
    if (done) break
  }
  buffer += decoder.decode()
  if (buffer.length > 0) onLine(buffer)
}
