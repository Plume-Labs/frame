// The Databases screen's reading of a FrameService of type postgres. Pure.
//
// Three settings move once a cluster exists, the ones below; the provider
// compares every other parameter with the live cluster and writes nothing on a
// difference, so the screen shows them without offering to edit them.

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
  fixed: { key: string; value: string }[]
  adopted: boolean
}

const FIXED: [string, string][] = [
  ['storageClass', 'Storage class'],
  ['postgresVersion', 'Postgres version'],
  ['database', 'Database'],
  ['owner', 'Owner'],
  ['walBucketClaim', 'WAL-G bucket claim'],
  ['walEndpoint', 'WAL-G endpoint'],
  ['walRetentionDays', 'WAL-G retention (days)'],
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
    fixed: FIXED.filter(([key]) => p[key]).map(([key, label]) => ({ key: label, value: p[key] })),
    adopted: Boolean(p.adoptUID),
  }
}

/** The schema's patterns, so a bad value is caught before the webhook's message. */
export function settingsProblem(s: DatabaseSettings): string | null {
  if (!/^[1-9]$/.test(s.instances)) return 'Instances: a number from 1 to 9.'
  if (!/^[1-9][0-9]*(Mi|Gi|Ti)$/.test(s.volumeSize)) return 'Volume size: for example 20Gi.'
  if (s.nodes && !/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?(,[a-z0-9]([-a-z0-9.]*[a-z0-9])?)*$/.test(s.nodes)) {
    return 'Nodes: node names separated by commas.'
  }
  return null
}

const UNITS: Record<string, number> = { Mi: 1, Gi: 1024, Ti: 1024 * 1024 }

function mebibytes(size: string): number {
  const m = /^(\d+)(Mi|Gi|Ti)$/.exec(size)
  return m ? Number(m[1]) * UNITS[m[2]] : NaN
}

/** What the confirmation has to say: each of these touches a running database. */
export function changeWarnings(db: Database, next: DatabaseSettings): string[] {
  const out: string[] = []
  if (mebibytes(next.volumeSize) < mebibytes(db.volumeSize)) out.push('A volume cannot shrink: the provider will refuse this.')
  else if (next.volumeSize !== db.volumeSize) out.push(`Volumes grow to ${next.volumeSize}. This cannot be undone.`)
  if (next.instances !== db.instances) {
    out.push(
      Number(next.instances) < Number(db.instances)
        ? `Goes down to ${next.instances} instance(s): the operator removes pods, and with them that redundancy.`
        : `Goes up to ${next.instances} instances: a replica copies the database, and pods restart when the spread is added.`,
    )
  }
  if (next.nodes !== db.nodes) out.push('Changing the nodes restarts the pods.')
  return out
}

export function changed(db: Database, next: DatabaseSettings): boolean {
  return next.instances !== db.instances || next.volumeSize !== db.volumeSize || next.nodes !== db.nodes
}

/** A merge patch: null removes `nodes`, which the schema's pattern would refuse empty. */
export function parametersPatch(next: DatabaseSettings): { spec: { parameters: Record<string, string | null> } } {
  return { spec: { parameters: { instances: next.instances, volumeSize: next.volumeSize, nodes: next.nodes || null } } }
}
