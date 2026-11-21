// The Databases screen's reading of a FrameService of type postgres. Pure.
//
// Three settings move once a cluster exists, the ones below; the provider
// compares every other parameter with the live cluster and writes nothing on a
// difference, so the screen shows them without offering to edit them.

import type { Message, MessageKey } from '@/i18n'

export interface FrameServiceCR {
  metadata: { name: string; namespace: string }
  spec?: { type?: string; parameters?: Record<string, string> }
  status?: {
    conditions?: { type: string; status: string; reason?: string; message?: string }[]
    binding?: { endpoint?: string }
  }
}

export interface DatabaseSettings {
  instances: string
  volumeSize: string
  nodes: string
}

export interface Database extends DatabaseSettings {
  name: string
  namespace: string
  ready: boolean
  reason: string
  message: string
  endpoint: string
  /** The parameters fixed at creation, for display. */
  fixed: { label: MessageKey; value: string }[]
  adopted: boolean
}

const FIXED: [string, MessageKey][] = [
  ['storageClass', 'db.fixed.storageClass'],
  ['postgresVersion', 'db.fixed.postgresVersion'],
  ['database', 'db.fixed.database'],
  ['owner', 'db.fixed.owner'],
  ['walBucketClaim', 'db.fixed.walBucketClaim'],
  ['walEndpoint', 'db.fixed.walEndpoint'],
  ['walRetentionDays', 'db.fixed.walRetentionDays'],
]

export function toDatabase(cr: FrameServiceCR): Database {
  const p = cr.spec?.parameters ?? {}
  const ready = cr.status?.conditions?.find((c) => c.type === 'Ready')
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    instances: p.instances ?? '',
    volumeSize: p.volumeSize ?? '',
    nodes: p.nodes ?? '',
    ready: ready?.status === 'True',
    reason: ready?.reason ?? 'NotReconciled',
    message: ready?.message ?? '',
    endpoint: cr.status?.binding?.endpoint ?? '',
    fixed: FIXED.filter(([key]) => p[key]).map(([key, label]) => ({ label, value: p[key] })),
    adopted: Boolean(p.adoptUID),
  }
}

/** The schema's patterns, so a bad value is caught before the webhook's message. */
export function settingsProblem(s: DatabaseSettings): Message | null {
  if (!/^[1-9]$/.test(s.instances)) return { key: 'db.problem.instances' }
  if (!/^[1-9][0-9]*(Mi|Gi|Ti)$/.test(s.volumeSize)) return { key: 'db.problem.volume' }
  if (s.nodes && !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?(,[a-z0-9]([-a-z0-9.]*[a-z0-9])?)*$/.test(s.nodes)) {
    return { key: 'db.problem.nodes' }
  }
  return null
}

const UNITS: Record<string, number> = { Mi: 1, Gi: 1024, Ti: 1024 * 1024 }

function mebibytes(size: string): number {
  const m = /^(\d+)(Mi|Gi|Ti)$/.exec(size)
  return m ? Number(m[1]) * UNITS[m[2]] : NaN
}

/** What the confirmation has to say: each of these touches a running database. */
export function changeWarnings(db: Database, next: DatabaseSettings): Message[] {
  const out: Message[] = []
  if (mebibytes(next.volumeSize) < mebibytes(db.volumeSize)) out.push({ key: 'svc.volumeCannotShrink' })
  else if (next.volumeSize !== db.volumeSize) out.push({ key: 'db.warn.grow', params: { size: next.volumeSize } })
  if (next.instances !== db.instances) {
    out.push(
      Number(next.instances) < Number(db.instances)
        ? { key: 'db.warn.down', params: { count: next.instances } }
        : { key: 'db.warn.up', params: { count: next.instances } },
    )
  }
  if (next.nodes !== db.nodes) out.push({ key: 'db.warn.nodes' })
  return out
}

export function changed(db: Database, next: DatabaseSettings): boolean {
  return next.instances !== db.instances || next.volumeSize !== db.volumeSize || next.nodes !== db.nodes
}

/** A merge patch: null removes `nodes`, which the schema's pattern would refuse empty. */
export function parametersPatch(next: DatabaseSettings): { spec: { parameters: Record<string, string | null> } } {
  return { spec: { parameters: { instances: next.instances, volumeSize: next.volumeSize, nodes: next.nodes || null } } }
}
