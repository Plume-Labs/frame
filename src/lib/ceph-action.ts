/**
 * FrameCephAction, projected for the "Ceph actions" screen: one everyday,
 * reversible Ceph gesture per object (in, out, restart, scrub, deep-scrub, a
 * flag for a while). The controller
 * (internal/controller/frame/framecephaction_controller.go) does every check
 * and sends the gesture once; this file only builds the object and says what
 * the screen shows.
 */

export type CephActionKind = 'OSDIn' | 'OSDOut' | 'OSDRestart' | 'OSDScrub' | 'OSDDeepScrub' | 'SetFlag'

/** The flags the console sets, each for a while (cephmgr.SettableFlags). */
export const SETTABLE_FLAGS = ['noout', 'norebalance', 'nobackfill', 'norecover', 'noscrub', 'nodeep-scrub'] as const
export type SettableFlag = (typeof SETTABLE_FLAGS)[number]

export const DEFAULT_FLAG_MINUTES = 120

export interface CephAction {
  name: string
  namespace: string
  createdAt: string
  action: CephActionKind
  osdID: number | null
  flag: string
  durationMinutes: number
  release: boolean
  phase: string
  message: string
  expiresAt: string | null
  flagWasSet: boolean
  /** When the gesture went out (null: not sent yet). */
  sentAt: string | null
}

/** The CR as the apiserver returns it. */
export interface FrameCephActionCR {
  metadata: { name: string; namespace: string; creationTimestamp?: string }
  spec?: { action?: string; osdID?: number; flag?: string; durationMinutes?: number; release?: boolean }
  status?: { phase?: string; message?: string; expiresAt?: string; flagWasSet?: boolean; sentAt?: string }
}

export function crToCephAction(cr: FrameCephActionCR): CephAction {
  const s = cr.status ?? {}
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    createdAt: cr.metadata.creationTimestamp ?? '',
    action: (cr.spec?.action ?? 'OSDIn') as CephActionKind,
    osdID: typeof cr.spec?.osdID === 'number' ? cr.spec.osdID : null,
    flag: cr.spec?.flag ?? '',
    durationMinutes: cr.spec?.durationMinutes ?? DEFAULT_FLAG_MINUTES,
    release: cr.spec?.release === true,
    phase: s.phase || 'Pending',
    message: s.message ?? '',
    expiresAt: s.expiresAt ?? null,
    flagWasSet: s.flagWasSet === true,
    sentAt: s.sentAt ?? null,
  }
}

export function isTerminalAction(phase: string): boolean {
  return phase === 'Done' || phase === 'Refused' || phase === 'Failed'
}

/**
 * What the list shows for a gesture: "Pending" only while nothing went out;
 * a sent gesture still waiting for its outcome (a restart rolling) is "In
 * progress", so nobody takes it for one that can still be withdrawn.
 */
export function actionLabel(a: CephAction): string {
  if (a.phase === 'Pending' && a.sentAt) return 'In progress'
  return a.phase
}

/** Grace after a flag's end before the banner calls it overdue. */
export const OVERDUE_GRACE_MS = 2 * 60_000

/** A held flag whose time is up but that Frame has not unset yet. */
export function isOverdue(a: CephAction, now: Date): boolean {
  if (a.phase !== 'Active' || a.flagWasSet || !a.expiresAt) return false
  return new Date(a.expiresAt).getTime() + OVERDUE_GRACE_MS < now.getTime()
}

/** The flags held now, for the banner: one per active SetFlag not yet released. */
export function activeFlags(items: CephAction[]): CephAction[] {
  return items
    .filter((a) => a.action === 'SetFlag' && a.phase === 'Active')
    .sort((a, b) => (a.expiresAt ?? '').localeCompare(b.expiresAt ?? ''))
}

/** What the gesture does, for the confirmation dialog. */
export function describeAction(action: CephActionKind, osd: number | null, flag: string, minutes: number): string {
  const o = `osd.${osd ?? '?'}`
  switch (action) {
    case 'OSDIn':
      return `Mark ${o} in: Ceph moves data back onto it.`
    case 'OSDOut':
      return `Mark ${o} out: Ceph copies its data to the other OSDs (one copy fewer meanwhile). Refused unless every PG is active+clean and every other OSD up and in, no other out or restart is under way (or ended less than 10 minutes ago), no OSD replacement is running, and no flag such as norecover or pause is set. Undo: mark it in.`
    case 'OSDRestart':
      return `Restart ${o} (its pod is replaced). Its PGs run on one copy fewer for a minute or two. Refused on the same conditions as an out.`
    case 'OSDScrub':
      return `Ask Ceph to scrub ${o} (light check of its PGs). Ceph runs it when its scrub schedule allows.`
    case 'OSDDeepScrub':
      return `Ask Ceph to deep-scrub ${o} (reads every object, heavy on the disk for a while).`
    case 'SetFlag':
      return `Set ${flag} on the whole cluster for ${minutes} minutes. Frame unsets it at the end, or when you release it. ${flagEffect(flag)}`
  }
}

function flagEffect(flag: string): string {
  switch (flag) {
    case 'noout':
      return 'Meanwhile a down OSD is not marked out: no data moves, but a failure is not healed either.'
    case 'norebalance':
      return 'Meanwhile Ceph does not rebalance data that is already safe.'
    case 'nobackfill':
      return 'Meanwhile no backfill runs: a missing copy is not rebuilt.'
    case 'norecover':
      return 'Meanwhile no recovery runs: a missing copy is not rebuilt.'
    case 'noscrub':
      return 'Meanwhile no scrub runs.'
    case 'nodeep-scrub':
      return 'Meanwhile no deep scrub runs.'
  }
  return ''
}

/** Why the form cannot be sent, or null. */
export function newActionError(action: CephActionKind, osd: string, minutes: string): string | null {
  if (action === 'SetFlag') {
    if (!/^\d{1,4}$/.test(minutes.trim())) return 'The duration is a number of minutes.'
    const m = Number(minutes)
    if (m < 5 || m > 1440) return 'Between 5 minutes and 24 hours.'
    return null
  }
  if (!/^\d{1,5}$/.test(osd.trim()) || Number(osd) > 65535) return 'The OSD is a number, e.g. 8 for osd.8.'
  return null
}

/** The new object. */
export function newActionBody(
  namespace: string,
  action: CephActionKind,
  osd: number | null,
  flag: SettableFlag | '',
  minutes: number,
  now: Date,
) {
  const stamp = now.toISOString().replace(/[-:]/g, '').replace(/\..*$/, '').replace('T', '-').toLowerCase()
  const what = action === 'SetFlag' ? flag : `osd-${osd}`
  const spec: Record<string, unknown> =
    action === 'SetFlag' ? { action, flag, durationMinutes: minutes } : { action, osdID: osd }
  return {
    apiVersion: 'frame.plume-labs.io/v1beta1',
    kind: 'FrameCephAction',
    metadata: { name: `${action.toLowerCase()}-${what}-${stamp}`, namespace },
    spec,
  }
}
