// Pure logic behind the Updates screen: what version is running and what an
// update would disturb right now. No network, no DOM — vitest reaches this
// file, not the .tsx screen. The screen files update requests; the in-cluster updater decides: it answers
// "can the cluster afford an update now", it does not perform one.

import type { ReleaseView, RunningComponent, UpdateRequestView } from './frame-sdk'

export interface ImageRef {
  repository: string
  /** Tag, or the short digest when the image is pinned by digest, or '' when untagged. */
  version: string
}

/** Split `registry:5000/frame-ui:abc123` into repository and version. */
export function parseImage(image: string): ImageRef {
  const at = image.indexOf('@')
  if (at >= 0) {
    const digest = image.slice(at + 1).replace(/^sha256:/, '')
    return { repository: image.slice(0, at), version: digest.slice(0, 12) }
  }
  // A colon after the last slash is a tag; one before it is a registry port.
  const colon = image.lastIndexOf(':')
  if (colon > image.lastIndexOf('/')) {
    return { repository: image.slice(0, colon), version: image.slice(colon + 1) }
  }
  return { repository: image, version: '' }
}

export interface VersionSummary {
  /** Distinct versions across the components, most common first. */
  versions: string[]
  /** True when the components do not all run one version (a rollout in flight, or a partial update). */
  mixed: boolean
  /** The most common version, '' when nothing is running. */
  dominant: string
}

export function summarizeVersions(components: RunningComponent[]): VersionSummary {
  const counts = new Map<string, number>()
  for (const c of components) {
    const v = parseImage(c.image).version || 'untagged'
    counts.set(v, (counts.get(v) ?? 0) + 1)
  }
  const versions = [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([v]) => v)
  return { versions, mixed: versions.length > 1, dominant: versions[0] ?? '' }
}

export interface ActivityCounts {
  runningJobs: number
  installsInProgress: number
  osdReplacementsActive: number
  unreadyComponents: number
}

export type DisturbanceKey = keyof ActivityCounts

export interface Disturbance {
  key: DisturbanceKey
  count: number
}

/** The reasons an update right now would hurt, empty when nothing is in flight. */
export function disturbances(counts: ActivityCounts): Disturbance[] {
  const order: DisturbanceKey[] = ['osdReplacementsActive', 'installsInProgress', 'runningJobs', 'unreadyComponents']
  return order.filter((key) => counts[key] > 0).map((key) => ({ key, count: counts[key] }))
}

export function countUnready(components: RunningComponent[]): number {
  return components.filter((c) => c.readyReplicas < c.desiredReplicas).length
}

/** A git commit id as typed by a person: 7 to 40 hex digits. Anything else is refused. */
export function isCommitId(s: string): boolean {
  return /^[0-9a-f]{7,40}$/.test(s)
}

/**
 * A version or commit as typed for another application: a tag or short digest
 * (letters, digits, dot, dash, underscore; starts alphanumeric). It lands in a
 * shell template, so nothing else is accepted.
 */
export function isVersionRef(s: string): boolean {
  return /^[A-Za-z0-9][\w.-]{0,63}$/.test(s)
}

export interface PreparationInput {
  /** What is being updated; defaults to Hearthrack. */
  name?: string
  /** A version tag is accepted for another application; Hearthrack takes a commit id only. */
  allowVersionRef?: boolean
  target: string
  components: RunningComponent[]
  reasons: Disturbance[]
  /** Site commands with `{sha}` and `{current}` placeholders (config `updates.prepareCommands`). */
  template: string
  gitopsUrl: string
  now: Date
}

/**
 * The text an operator copies to prepare an update. It prepares only: it
 * changes nothing and opens no pull request. The cluster is updated by the
 * GitOps pipeline after review, and the "moment" snapshot is dated because it
 * is read now and the sync happens later.
 */
export function buildPreparation(input: PreparationInput): string {
  const { target, components, reasons, template, gitopsUrl, now } = input
  const name = input.name ?? 'Hearthrack'
  if (!(input.allowVersionRef ? isVersionRef(target) : isCommitId(target))) throw new Error('target is not a valid version')
  // The version comes from the cluster and lands in a shell template: keep only
  // characters a tag or short digest can carry.
  const current = (summarizeVersions(components).dominant || 'unknown').replace(/[^\w.-]/g, '_')
  const lines: string[] = [
    `# ${name.replace(/[\r\n]/g, ' ')} update to ${target}`,
    `# Prepared ${now.toISOString()} from the console. Nothing has been changed.`,
    `# Running now: ${current}`,
  ]
  for (const c of components) lines.push(`#   ${c.name.replace(/[\r\n]/g, ' ')}: ${c.image.replace(/[\r\n]/g, ' ')}`)
  lines.push(
    reasons.length === 0
      ? '# At that moment nothing was in flight. Check again before the sync.'
      : `# In flight at that moment: ${reasons.map((r) => `${r.key}=${r.count}`).join(', ')}. Check again before the sync.`,
  )
  if (gitopsUrl) lines.push(`# GitOps repository: ${gitopsUrl}`)
  lines.push('', template.split('{sha}').join(target).split('{current}').join(current))
  return lines.join('\n')
}

// ── One-click update: what the console decides on its own ────────────────────
//
// The console decides nothing that matters for safety: signatures, version
// rules and "is it safe now" belong to the in-cluster updater, which refuses a
// request it cannot honour. What follows only decides what to *show*: which
// release is on offer, what is running, whether a request is still in flight.

/** Semver precedence for "1.2.3" and "1.2.3-beta.1". Returns NaN for a string that is not a version. */
export function compareVersions(a: string, b: string): number {
  const parse = (s: string) => {
    const m = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(s)
    return m ? { n: [Number(m[1]), Number(m[2]), Number(m[3])], pre: m[4] ? m[4].split('.') : [] } : null
  }
  const x = parse(a)
  const y = parse(b)
  if (!x || !y) return NaN
  for (let i = 0; i < 3; i++) if (x.n[i] !== y.n[i]) return x.n[i] < y.n[i] ? -1 : 1
  if (x.pre.length === 0 || y.pre.length === 0) return x.pre.length === y.pre.length ? 0 : x.pre.length === 0 ? 1 : -1
  for (let i = 0; i < Math.min(x.pre.length, y.pre.length); i++) {
    const p = x.pre[i]
    const q = y.pre[i]
    if (p === q) continue
    const pn = /^\d+$/.test(p)
    const qn = /^\d+$/.test(q)
    if (pn && qn) return Number(p) < Number(q) ? -1 : 1
    if (pn) return -1
    if (qn) return 1
    return p < q ? -1 : 1
  }
  return x.pre.length === y.pre.length ? 0 : x.pre.length < y.pre.length ? -1 : 1
}

/** An update request the updater has not finished with. */
export function isInFlight(r: UpdateRequestView): boolean {
  return r.phase === '' || ['Pending', 'Verifying', 'Applying', 'RollingBack'].includes(r.phase)
}

/** The request being worked on now, if any (newest first in `requests`). */
export function activeRequest(requests: UpdateRequestView[]): UpdateRequestView | null {
  return requests.find(isInFlight) ?? null
}

/**
 * The version running now. A successful request lists only the components whose
 * image changed, so history alone is ambiguous when a later release reuses an
 * image: the running images decide, and history only breaks a tie. '' when it
 * cannot be told (a fresh install the updater has not touched).
 */
export function currentVersion(requests: UpdateRequestView[], releases: ReleaseView[], runningImages: string[] = []): string {
  const running = new Set(runningImages)
  const scored = releases
    .map((rel) => ({ rel, n: Object.values(rel.images).filter((img) => running.has(img)).length }))
    .filter((x) => x.n > 0)
  const best = Math.max(0, ...scored.map((x) => x.n))
  const top = scored.filter((x) => x.n === best).map((x) => x.rel)
  if (top.length === 1) return top[0].version
  const last = requests.find((r) => r.phase === 'Succeeded')
  if (!last || last.components.length === 0) return ''
  const byHistory = (rel: ReleaseView) => last.components.every((c) => rel.images[c.name] === c.to)
  const pool = top.length > 0 ? top : releases
  const hits = pool.filter(byHistory)
  return hits.length === 1 ? hits[0].version : ''
}

/**
 * The release to offer: the newest verified, not-withdrawn one on the chosen
 * channel that is newer than what runs. With no known running version every
 * such release qualifies, and the updater answers "already current" if it is.
 */
export function availableRelease(releases: ReleaseView[], current: string, channel: 'stable' | 'beta'): ReleaseView | null {
  const ok = releases
    .filter((r) => r.verified && !r.revoked && r.version !== '')
    // beta sees both channels; stable sees only stable
    .filter((r) => channel === 'beta' || r.channel === 'stable')
    .filter((r) => current === '' || compareVersions(r.version, current) > 0)
    .sort((a, b) => compareVersions(b.version, a.version))
  return ok[0] ?? null
}

/**
 * The version a rollback would return to, or null when none is offered: the
 * last successful update must have started from a known version that the
 * running release says is safe to return to.
 */
export function rollbackTarget(requests: UpdateRequestView[], releases: ReleaseView[], current: string): string | null {
  if (current === '') return null
  const last = requests.find((r) => r.phase === 'Succeeded')
  // After a rollback there is nothing further back to offer: the previous version is the one now running.
  if (!last || last.rollback || last.fromVersion === '') return null
  const running = releases.find((r) => r.version === current)
  if (!running || running.rollbackSafeTo === '') return null
  return compareVersions(last.fromVersion, running.rollbackSafeTo) >= 0 ? last.fromVersion : null
}
