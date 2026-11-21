// The Caches screen's reading of a FrameService of type cache. Pure.
//
// maxMemory and, for a queue, volumeSize are what moves once an instance
// exists; the provider degrades on any other change, so the screen shows the
// rest without offering to edit it.

import type { FrameServiceCR } from './databases'
import type { Message } from '@/i18n'

export interface CacheSettings {
  maxMemory: string
  volumeSize: string
}

export interface Cache extends CacheSettings {
  name: string
  namespace: string
  profile: string
  storageClass: string
  ready: boolean
  reason: string
  message: string
  endpoint: string
}

export function toCache(cr: FrameServiceCR): Cache {
  const p = cr.spec?.parameters ?? {}
  const ready = cr.status?.conditions?.find((c) => c.type === 'Ready')
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    profile: p.profile ?? '',
    maxMemory: p.maxMemory ?? '',
    volumeSize: p.volumeSize ?? '',
    storageClass: p.storageClass ?? '',
    ready: ready?.status === 'True',
    reason: ready?.reason ?? 'NotReconciled',
    message: ready?.message ?? '',
    endpoint: cr.status?.binding?.endpoint ?? '',
  }
}

const UNITS: Record<string, number> = { Mi: 1, Gi: 1024, Ti: 1024 * 1024 }

function mebibytes(size: string): number {
  const m = /^(\d+)(Mi|Gi|Ti)$/.exec(size)
  return m ? Number(m[1]) * UNITS[m[2]] : NaN
}

/** The schema's patterns and the provider's rule, before the webhook's message. */
export function settingsProblem(c: Cache, s: CacheSettings): Message | null {
  if (!/^[1-9][0-9]{0,6}(Mi|Gi)$/.test(s.maxMemory)) return { key: 'cache.problem.maxMemory' }
  if (c.profile !== 'queue') return null
  if (!/^[1-9][0-9]{0,6}(Mi|Gi|Ti)$/.test(s.volumeSize)) return { key: 'cache.problem.volume' }
  if (mebibytes(s.volumeSize) < 3 * mebibytes(s.maxMemory)) return { key: 'cache.problem.volumeRatio' }
  return null
}

/** What the confirmation has to say: each of these touches a running server. */
export function changeWarnings(c: Cache, next: CacheSettings): Message[] {
  const out: Message[] = []
  if (next.maxMemory !== c.maxMemory) {
    out.push(
      c.profile === 'queue'
        ? { key: 'cache.warn.restartQueue' }
        : { key: 'cache.warn.restartCache' },
    )
  }
  if (c.profile === 'queue' && mebibytes(next.maxMemory) < mebibytes(c.maxMemory)) {
    out.push({ key: 'cache.warn.killed' })
  }
  if (mebibytes(next.volumeSize) < mebibytes(c.volumeSize)) out.push({ key: 'svc.volumeCannotShrink' })
  else if (next.volumeSize !== c.volumeSize) out.push({ key: 'cache.warn.grow', params: { size: next.volumeSize } })
  return out
}

export function changed(c: Cache, next: CacheSettings): boolean {
  return next.maxMemory !== c.maxMemory || next.volumeSize !== c.volumeSize
}

export function parametersPatch(c: Cache, next: CacheSettings): { spec: { parameters: Record<string, string> } } {
  const parameters: Record<string, string> = { maxMemory: next.maxMemory }
  if (c.profile === 'queue') parameters.volumeSize = next.volumeSize
  return { spec: { parameters } }
}
