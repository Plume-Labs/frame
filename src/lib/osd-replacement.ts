/**
 * FrameOSDReplacement, projected for the "Replace an OSD" screen.
 *
 * The controller (internal/controller/frame/frameosdreplacement_controller.go)
 * does every step and every check; this file only says, for the screen,
 * which step a replacement is at and what — if anything — a person has to
 * do. Two steps wait for a person: approval no. 1 (the old disk's serial,
 * retyped, purges the OSD) and approval no. 2 (the new disk's serial, read on
 * its label and retyped, wipes it). Neither can be undone.
 */

/** A disk as the replacement identified it (status.oldDisk / newDisk). */
export interface ReplacementDisk {
  byIdPath: string
  serial: string
  wwn: string
  path: string
  sizeGB: number
  model: string
  logicalSectorBytes: number
  smartPassed: boolean | null
}

export interface ReplacementEvent {
  at: string
  phase: string
  message: string
}

export interface OSDReplacement {
  name: string
  namespace: string
  createdAt: string
  osdID: number
  serial: string
  purgeApprovalSerial: string
  wipeApprovalSerial: string
  cancel: boolean
  resume: number
  phase: string
  pausedPhase: string
  observedResume: number
  message: string
  phaseSince: string | null
  host: string
  machineRef: string
  oldDisk: ReplacementDisk | null
  newDisk: ReplacementDisk | null
  pgs: string
  purgeRequested: boolean
  events: ReplacementEvent[]
}

/** The CR as the apiserver returns it. */
export interface FrameOSDReplacementCR {
  metadata: { name: string; namespace: string; creationTimestamp?: string }
  spec?: {
    osdID?: number
    serial?: string
    purgeApproval?: { serial?: string }
    wipeApproval?: { serial?: string }
    cancel?: boolean
    resume?: number
  }
  status?: {
    phase?: string
    pausedPhase?: string
    observedResume?: number
    message?: string
    phaseSince?: string
    host?: string
    machineRef?: string
    oldDisk?: RawDisk
    newDisk?: RawDisk
    pgs?: string
    purgeRequestedAt?: string
    events?: { at?: string; phase?: string; message?: string }[]
  }
}

interface RawDisk {
  byIdPath?: string
  serial?: string
  wwn?: string
  path?: string
  sizeGB?: number
  model?: string
  logicalSectorBytes?: number
  smart?: { passed?: boolean }
}

function toDisk(d: RawDisk | undefined): ReplacementDisk | null {
  if (!d) return null
  return {
    byIdPath: d.byIdPath ?? '',
    serial: d.serial ?? '',
    wwn: d.wwn ?? '',
    path: d.path ?? '',
    sizeGB: d.sizeGB ?? 0,
    model: d.model ?? '',
    logicalSectorBytes: d.logicalSectorBytes ?? 0,
    smartPassed: typeof d.smart?.passed === 'boolean' ? d.smart.passed : null,
  }
}

export function crToReplacement(cr: FrameOSDReplacementCR): OSDReplacement {
  const s = cr.status ?? {}
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    createdAt: cr.metadata.creationTimestamp ?? '',
    osdID: cr.spec?.osdID ?? -1,
    serial: cr.spec?.serial ?? '',
    purgeApprovalSerial: cr.spec?.purgeApproval?.serial ?? '',
    wipeApprovalSerial: cr.spec?.wipeApproval?.serial ?? '',
    cancel: cr.spec?.cancel === true,
    resume: cr.spec?.resume ?? 0,
    phase: s.phase || 'Checking',
    pausedPhase: s.pausedPhase ?? '',
    observedResume: s.observedResume ?? 0,
    message: s.message ?? '',
    phaseSince: s.phaseSince ?? null,
    host: s.host ?? '',
    machineRef: s.machineRef ?? '',
    oldDisk: toDisk(s.oldDisk),
    newDisk: toDisk(s.newDisk),
    pgs: s.pgs ?? '',
    purgeRequested: !!s.purgeRequestedAt,
    events: (s.events ?? []).map((e) => ({ at: e.at ?? '', phase: e.phase ?? '', message: e.message ?? '' })),
  }
}

/** The steps, in order, as the screen names them. */
export const REPLACEMENT_STEPS: { phase: string; label: string; human?: boolean }[] = [
  { phase: 'Checking', label: 'Checks' },
  { phase: 'Draining', label: 'Drain (out)' },
  { phase: 'AwaitingPurgeApproval', label: 'Approval 1: purge', human: true },
  { phase: 'Purging', label: 'Purge' },
  { phase: 'AwaitingDisk', label: 'Swap the disk', human: true },
  { phase: 'AwaitingWipeApproval', label: 'Approval 2: wipe', human: true },
  { phase: 'Wiping', label: 'Wipe' },
  { phase: 'Declaring', label: 'Declare to Rook' },
  { phase: 'AwaitingOSD', label: 'New OSD' },
  { phase: 'Backfilling', label: 'Backfill' },
]

export const TERMINAL_PHASES = ['Succeeded', 'Cancelled', 'Failed']

export function isTerminal(phase: string): boolean {
  return TERMINAL_PHASES.includes(phase)
}

/** The step the replacement is at (a paused one: the step it paused in). */
export function currentStepIndex(r: OSDReplacement): number {
  if (r.phase === 'Succeeded') return REPLACEMENT_STEPS.length
  const phase = r.phase === 'Paused' ? r.pausedPhase : r.phase
  const i = REPLACEMENT_STEPS.findIndex((s) => s.phase === phase)
  return i < 0 ? 0 : i
}

export type StepState = 'done' | 'current' | 'paused' | 'pending' | 'stopped'

export function stepStates(r: OSDReplacement): StepState[] {
  const cur = currentStepIndex(r)
  return REPLACEMENT_STEPS.map((_, i) => {
    if (i < cur) return 'done'
    if (i > cur) return 'pending'
    if (r.phase === 'Paused') return 'paused'
    if (r.phase === 'Cancelled' || r.phase === 'Failed') return 'stopped'
    return 'current'
  })
}

/**
 * What the person can do now. A cancel before the purge marks the OSD in
 * again; after it, nothing is undone (the screen says so).
 */
export interface ReplacementActions {
  approvePurge: boolean
  approveWipe: boolean
  resume: boolean
  cancel: boolean
  cancelUndoes: boolean
}

export function availableActions(r: OSDReplacement): ReplacementActions {
  const live = !isTerminal(r.phase) && !r.cancel
  return {
    approvePurge: live && r.phase === 'AwaitingPurgeApproval',
    approveWipe: live && r.phase === 'AwaitingWipeApproval',
    resume: live && r.phase === 'Paused' && r.resume <= r.observedResume,
    cancel: live,
    cancelUndoes: !r.purgeRequested,
  }
}

/** A serial as typed: spaces around it are not part of it, nothing else is changed. */
export function normalizeSerial(s: string): string {
  return s.trim()
}

/** The new object for "replace osd.N whose disk has serial S". */
export function newReplacementBody(namespace: string, osdID: number, serial: string, now: Date) {
  const stamp = now.toISOString().replace(/[-:]/g, '').replace(/\..*$/, '').replace('T', '-').toLowerCase()
  return {
    apiVersion: 'frame.plume-labs.io/v1beta1',
    kind: 'FrameOSDReplacement',
    metadata: { name: `osd-${osdID}-${stamp}`, namespace },
    spec: { osdID, serial: normalizeSerial(serial) },
  }
}

/** Why the form cannot be sent, or null. */
export function newReplacementError(osd: string, serial: string): string | null {
  if (!/^\d{1,5}$/.test(osd.trim()) || Number(osd) > 65535) return 'The OSD is a number, e.g. 8 for osd.8.'
  const s = normalizeSerial(serial)
  if (!s) return 'Type the serial of the disk behind this OSD, as printed on its label.'
  if (s.length > 128) return 'A serial is at most 128 characters.'
  return null
}
