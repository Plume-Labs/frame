import type { MessageKey, Params } from '@/i18n'

/**
 * What to tell an operator when reading from the cluster failed, by the HTTP
 * status the read ended with. One generic "Cannot reach the Kubernetes API"
 * for everything sent people to check the proxy when the real answer was "your
 * role may not read this" (403) or "this cluster does not serve it" (404).
 *
 * The copy is returned as message keys (plus parameters), not text, so it is
 * rendered in the language the console is showing: `t(copy.title)`,
 * `t(copy.hint, copy.params)`.
 */
export interface LoadErrorCopy {
  title: MessageKey
  hint: MessageKey
  params?: Params
}

export function describeLoadError(status: number | undefined): LoadErrorCopy {
  switch (status) {
    case 401:
      return { title: 'loadError.401.title', hint: 'loadError.401.hint' }
    case 403:
      return { title: 'loadError.403.title', hint: 'loadError.403.hint' }
    case 404:
      return { title: 'loadError.404.title', hint: 'loadError.404.hint' }
    default:
      if (status !== undefined && (status === 429 || status >= 500)) {
        return { title: 'loadError.server.title', hint: 'loadError.server.hint' }
      }
      if (status !== undefined && status >= 400) {
        return { title: 'loadError.rejected.title', hint: 'loadError.rejected.hint', params: { status } }
      }
      return { title: 'loadError.unreachable.title', hint: 'loadError.unreachable.hint' }
  }
}
