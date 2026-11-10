import { describe, expect, it } from 'vitest'
import { activeFlags, crToCephAction, describeAction, newActionBody, newActionError, type FrameCephActionCR } from './ceph-action'

function cr(spec: FrameCephActionCR['spec'], status?: FrameCephActionCR['status']): FrameCephActionCR {
  return { metadata: { name: 'x', namespace: 'frame-system' }, spec, status }
}

describe('ceph actions', () => {
  it('maps the CR, with no status as Pending', () => {
    const a = crToCephAction(cr({ action: 'OSDOut', osdID: 8 }))
    expect(a.phase).toBe('Pending')
    expect(a.osdID).toBe(8)
    expect(a.flag).toBe('')
  })

  it('lists the flags held now, soonest end first', () => {
    const items = [
      crToCephAction(cr({ action: 'SetFlag', flag: 'noout' }, { phase: 'Active', expiresAt: '2026-11-13T20:00:00Z' })),
      crToCephAction(cr({ action: 'SetFlag', flag: 'noscrub' }, { phase: 'Done' })),
      crToCephAction(cr({ action: 'SetFlag', flag: 'nobackfill' }, { phase: 'Active', expiresAt: '2026-11-13T19:00:00Z' })),
      crToCephAction(cr({ action: 'OSDOut', osdID: 8 }, { phase: 'Done' })),
    ]
    expect(activeFlags(items).map((a) => a.flag)).toEqual(['nobackfill', 'noout'])
  })

  it('validates the form', () => {
    expect(newActionError('OSDOut', '8', '')).toBeNull()
    expect(newActionError('OSDOut', 'osd.8', '')).not.toBeNull()
    expect(newActionError('SetFlag', '', '120')).toBeNull()
    expect(newActionError('SetFlag', '', '2')).not.toBeNull()
    expect(newActionError('SetFlag', '', '2000')).not.toBeNull()
  })

  it('builds an object the CRD accepts: osdID or flag, never both', () => {
    const now = new Date('2026-11-13T18:04:05.123Z')
    const out = newActionBody('frame-system', 'OSDOut', 8, '', 0, now)
    expect(out.spec).toEqual({ action: 'OSDOut', osdID: 8 })
    expect(out.metadata.name).toMatch(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/)
    const flag = newActionBody('frame-system', 'SetFlag', null, 'nodeep-scrub', 30, now)
    expect(flag.spec).toEqual({ action: 'SetFlag', flag: 'nodeep-scrub', durationMinutes: 30 })
    expect(flag.metadata.name).toBe('setflag-nodeep-scrub-20261113-180405')
  })

  it('says what out and restart cost', () => {
    expect(describeAction('OSDOut', 8, '', 0)).toContain('active+clean')
    expect(describeAction('SetFlag', null, 'noout', 120)).toContain('120 minutes')
  })
})
