/**
 * What changed in an edited manifest, phrased for the audit trail.
 *
 * Without this, every edit records as `update deployments/api` — the same
 * string for a replica bump and for adding a `hostPath: /` volume, which is
 * the one write in the product where that distinction matters most.
 *
 * It compares objects, not text: the editor holds YAML, and the apiserver is
 * what turns YAML into an object (a `PUT ?dryRun=All`, whose response is what
 * would be stored). That is why this repository needs no YAML parser and why
 * the comparison is trustworthy — the "after" side is the server's own reading
 * of the text, not ours.
 */

/**
 * FrameTaskSpec.Action's CRD cap.
 *
 * Not advisory. A longer value makes the apiserver refuse the FrameTask
 * create; `TaskRecorder.Start` logs the error and returns "", and the user's
 * edit proceeds. So an over-long label does not produce a truncated record —
 * it produces no record, on exactly the large edits that most deserve one.
 */
export const MAX_ACTION_LENGTH = 200

/**
 * Paths the server owns, which therefore differ between a read and a dry run
 * on every single request.
 *
 * Left in, they are always the first three paths alphabetically after
 * `metadata.labels`, so they would spend the whole three-path budget and push
 * the actual change into the "+N more" counter on every edit.
 */
export const IGNORED_PATHS: readonly string[] = [
  'metadata.creationTimestamp',
  'metadata.generation',
  'metadata.managedFields',
  'metadata.resourceVersion',
  'metadata.uid',
  'status',
]

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function walk(before: unknown, after: unknown, path: string, out: string[]): void {
  if (path !== '' && IGNORED_PATHS.includes(path)) return
  if (Object.is(before, after)) return

  if (isRecord(before) && isRecord(after)) {
    for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) {
      walk(before[key], after[key], path === '' ? key : `${path}.${key}`, out)
    }
    return
  }
  if (Array.isArray(before) && Array.isArray(after)) {
    for (let i = 0; i < Math.max(before.length, after.length); i += 1) {
      walk(before[i], after[i], `${path}[${i}]`, out)
    }
    return
  }
  // Everything else — a scalar that moved, a field added or removed, a value
  // whose *shape* changed — is reported at this path and not descended into.
  // An added block is one decision a person made, and listing its leaves would
  // spend the label's budget describing a paste.
  if (JSON.stringify(before) === JSON.stringify(after)) return
  out.push(path === '' ? '(the whole object)' : path)
}

/** Every path at which `after` differs from `before`, sorted. */
export function changedFieldPaths(before: unknown, after: unknown): string[] {
  const out: string[] = []
  walk(before, after, '', out)
  return out.sort()
}

function truncate(s: string): string {
  return s.length <= MAX_ACTION_LENGTH ? s : s.slice(0, MAX_ACTION_LENGTH)
}

/**
 * The `X-Frame-Action` label for an edit.
 *
 * Three paths then a count: three is what fits alongside a namespaced name
 * inside the cap, and the count is more honest than a longer list truncated
 * mid-path.
 */
export function editActionLabel(
  kind: string,
  namespace: string,
  name: string,
  paths: string[],
): string {
  const head = `edit ${kind.toLowerCase()} ${namespace}/${name}`
  if (paths.length === 0) return truncate(`${head}: no field changed`)
  const shown = paths.slice(0, 3)
  const rest = paths.length - shown.length
  const body = rest > 0 ? `${shown.join(', ')} +${rest} more` : shown.join(', ')
  return truncate(`${head}: ${body}`)
}
