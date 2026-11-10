import { describe, expect, it } from 'vitest'
import {
  availableActions,
  crToReplacement,
  currentStepIndex,
  newReplacementBody,
  newReplacementError,
  REPLACEMENT_STEPS,
  stepStates,
  type FrameOSDReplacementCR,
} from './osd-replacement'

function cr(status: FrameOSDReplacementCR['status'], spec: FrameOSDReplacementCR['spec'] = {}): FrameOSDReplacementCR {
  return {
    metadata: { name: 'osd-8-x', namespace: 'frame-system', creationTimestamp: '2026-11-13T18:00:00Z' },
    spec: { osdID: 8, serial: 'Z2AW5NWA', ...spec },
    status,
  }
}

describe('osd replacement projection', () => {
  it('maps the CR, with no status as Checking', () => {
    const r = crToReplacement(cr(undefined))
    expect(r.phase).toBe('Checking')
    expect(r.osdID).toBe(8)
    expect(r.oldDisk).toBeNull()
    expect(currentStepIndex(r)).toBe(0)
  })

  it('places a paused replacement on the step it paused in', () => {
    const r = crToReplacement(cr({ phase: 'Paused', pausedPhase: 'Purging' }))
    const i = REPLACEMENT_STEPS.findIndex((s) => s.phase === 'Purging')
    expect(currentStepIndex(r)).toBe(i)
    const states = stepStates(r)
    expect(states[i]).toBe('paused')
    expect(states[i - 1]).toBe('done')
    expect(states[i + 1]).toBe('pending')
  })

  it('marks every step done once succeeded', () => {
    const r = crToReplacement(cr({ phase: 'Succeeded' }))
    expect(stepStates(r).every((s) => s === 'done')).toBe(true)
  })

  it('offers each approval only at its own step', () => {
    const purge = availableActions(crToReplacement(cr({ phase: 'AwaitingPurgeApproval' })))
    expect(purge.approvePurge).toBe(true)
    expect(purge.approveWipe).toBe(false)
    const wipe = availableActions(crToReplacement(cr({ phase: 'AwaitingWipeApproval', purgeRequestedAt: 'x' })))
    expect(wipe.approveWipe).toBe(true)
    expect(wipe.approvePurge).toBe(false)
    expect(wipe.cancelUndoes).toBe(false)
  })

  it('offers resume only while paused and not already asked', () => {
    expect(availableActions(crToReplacement(cr({ phase: 'Paused', observedResume: 1 }, { resume: 1 }))).resume).toBe(true)
    expect(availableActions(crToReplacement(cr({ phase: 'Paused', observedResume: 1 }, { resume: 2 }))).resume).toBe(false)
    expect(availableActions(crToReplacement(cr({ phase: 'Draining' }))).resume).toBe(false)
  })

  it('offers nothing once terminal or cancelled', () => {
    for (const phase of ['Succeeded', 'Cancelled', 'Failed']) {
      const a = availableActions(crToReplacement(cr({ phase })))
      expect(a.cancel || a.resume || a.approvePurge || a.approveWipe).toBe(false)
    }
    expect(availableActions(crToReplacement(cr({ phase: 'AwaitingPurgeApproval' }, { cancel: true }))).approvePurge).toBe(false)
  })

  it('validates the form', () => {
    expect(newReplacementError('8', 'Z2AW5NWA')).toBeNull()
    expect(newReplacementError('osd.8', 'Z2AW5NWA')).not.toBeNull()
    expect(newReplacementError('70000', 'Z2AW5NWA')).not.toBeNull()
    expect(newReplacementError('8', '   ')).not.toBeNull()
  })

  it('builds a valid object name and trims the serial', () => {
    const b = newReplacementBody('frame-system', 8, '  Z2AW5NWA ', new Date('2026-11-13T18:04:05.123Z'))
    expect(b.metadata.name).toBe('osd-8-20261113-180405')
    expect(b.metadata.name).toMatch(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/)
    expect(b.spec).toEqual({ osdID: 8, serial: 'Z2AW5NWA' })
  })
})
