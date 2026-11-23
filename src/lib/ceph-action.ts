/**
 * FrameCephAction, projected for the "Ceph actions" screen: one everyday,
 * reversible Ceph gesture per object (in, out, restart, scrub, deep-scrub, a
 * flag for a while). The controller
 * (internal/controller/frame/framecephaction_controller.go) does every check
 * and sends the gesture once; this file only builds the object and says what
 * the screen shows.
 */

import type { Message, MessageKey } from '@/i18n'

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
export function actionLabel(a: CephAction): Message | string {
  if (a.phase === 'Pending' && a.sentAt) return { key: 'cax.phase.inProgress' }
  const key = PHASE_LABEL[a.phase]
  return key ? { key } : a.phase
}

const PHASE_LABEL: Record<string, MessageKey> = {
  Pending: 'cax.phase.pending',
  Active: 'cax.phase.active',
  Done: 'cax.phase.done',
  Refused: 'cax.phase.refused',
  Failed: 'cax.phase.failed',
}

/** Grace after a flag's end before the banner calls it overdue. */
export const OVERDUE_GRACE_MS = 2 * 60_000

/** A held flag whose time is up but that Hearthrack has not unset yet. */
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
export function describeAction(action: CephActionKind, osd: number | null, flag: string, minutes: number): Message {
  const o = `osd.${osd ?? '?'}`
  switch (action) {
    case 'OSDIn':
      return { key: 'cax.desc.in', params: { osd: o } }
    case 'OSDOut':
      return { key: 'cax.desc.out', params: { osd: o } }
    case 'OSDRestart':
      return { key: 'cax.desc.restart', params: { osd: o } }
    case 'OSDScrub':
      return { key: 'cax.desc.scrub', params: { osd: o } }
    case 'OSDDeepScrub':
      return { key: 'cax.desc.deepScrub', params: { osd: o } }
    case 'SetFlag':
      return { key: 'cax.desc.flag', params: { flag, minutes, effect: flagEffect(flag) } }
  }
}

function flagEffect(flag: string): Message | '' {
  switch (flag) {
    case 'noout':
      return { key: 'cax.flag.noout' }
    case 'norebalance':
      return { key: 'cax.flag.norebalance' }
    case 'nobackfill':
      return { key: 'cax.flag.nobackfill' }
    case 'norecover':
      return { key: 'cax.flag.norecover' }
    case 'noscrub':
      return { key: 'cax.flag.noscrub' }
    case 'nodeep-scrub':
      return { key: 'cax.flag.nodeepscrub' }
  }
  return ''
}

/** Why the form cannot be sent, or null. */
export function newActionError(action: CephActionKind, osd: string, minutes: string): Message | null {
  if (action === 'SetFlag') {
    if (!/^\d{1,4}$/.test(minutes.trim())) return { key: 'cax.err.minutesNumber' }
    const m = Number(minutes)
    if (m < 5 || m > 1440) return { key: 'cax.err.minutesRange' }
    return null
  }
  if (!/^\d{1,5}$/.test(osd.trim()) || Number(osd) > 65535) return { key: 'cax.err.osdNumber' }
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
