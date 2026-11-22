// Pure logic behind the Updates screen: what version is running and what an
// update would disturb right now. No network, no DOM — vitest reaches this
// file, not the .tsx screen. The screen is read-only on purpose: it answers
// "can the cluster afford an update now", it does not perform one.

import type { RunningComponent } from './frame-sdk'

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
  for (const c of components) lines.push(`#   ${c.name}: ${c.image}`)
  lines.push(
    reasons.length === 0
      ? '# At that moment nothing was in flight. Check again before the sync.'
      : `# In flight at that moment: ${reasons.map((r) => `${r.key}=${r.count}`).join(', ')}. Check again before the sync.`,
  )
  if (gitopsUrl) lines.push(`# GitOps repository: ${gitopsUrl}`)
  lines.push('', template.split('{sha}').join(target).split('{current}').join(current))
  return lines.join('\n')
}
