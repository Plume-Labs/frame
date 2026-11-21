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
