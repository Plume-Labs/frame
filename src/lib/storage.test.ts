import { describe, it, expect } from 'vitest'
import { describeDivergence, capacityLine, claimGapLine, cephWarningReasons } from './storage'

describe('describeDivergence', () => {
  it('nomme la baie pour un disque que seul le BMC voit', () => {
    const line = describeDivergence({
      serialNumber: 'W4722RRA',
      reason: 'bmc-only',
      detail: 'bay 2I:6:8: the BMC lists it, the node kernel does not',
    })
    expect(line).toContain('W4722RRA')
    expect(line).toContain('2I:6:8')
  })

  it('ne dit jamais qu-un disque bmc-only est en panne', () => {
    // The BMC reports Health OK for the masked disk. A screen that renders
    // a divergence as a hardware fault sends someone to replace a healthy
    // drive.
    const line = describeDivergence({ serialNumber: 'W4722RRA', reason: 'bmc-only', detail: '' })
    expect(line.toLowerCase()).not.toContain('fail')
    expect(line.toLowerCase()).not.toContain('error')
  })

  it('distingue os-only de bmc-only', () => {
    const a = describeDivergence({ serialNumber: 'X', reason: 'bmc-only', detail: '' })
    const b = describeDivergence({ serialNumber: 'X', reason: 'os-only', detail: '' })
    expect(a).not.toEqual(b)
  })
})

describe('capacityLine', () => {
  it('rend l-utilisable en premier', () => {
    const line = capacityLine({ usable: '1.2Ti', used: '480Gi', raw: '3.6Ti' })
    expect(line.indexOf('1.2Ti')).toBeLessThan(line.indexOf('3.6Ti'))
  })

  it('ne rend jamais le brut seul', () => {
    // This is the capacity incident, in one assertion: OSDs were sized on
    // a raw number while replication divided it by three.
    const line = capacityLine({ usable: '', used: '', raw: '3.6Ti' })
    expect(line).not.toEqual('3.6Ti')
    expect(line.toLowerCase()).toContain('unknown')
  })

  it('se passe du brut quand il manque', () => {
    expect(capacityLine({ usable: '1.2Ti', used: '480Gi', raw: '' })).toContain('1.2Ti')
  })
})

describe('claimGapLine', () => {
  it('rend l-ecart lisible', () => {
    expect(claimGapLine('ceph-rbd', { total: 16, labelled: 0 })).toContain('16')
    expect(claimGapLine('ceph-rbd', { total: 16, labelled: 0 })).toContain('0')
  })

  it('ne presente pas zero etiquette comme une faute', () => {
    // Enforcement is opt-in per object; the normal state on day one is
    // many claims and no labels.
    const line = claimGapLine('ceph-rbd', { total: 16, labelled: 0 }).toLowerCase()
    expect(line).not.toContain('error')
    expect(line).not.toContain('violation')
  })
})

describe('cephWarningReasons', () => {
  it('extrait les verifications en echec', () => {
    const reasons = cephWarningReasons({
      health: 'HEALTH_WARN',
      checks: {
        POOL_NO_REDUNDANCY: { summary: { message: '1 pool(s) have no replicas configured' } },
        MON_CLOCK_SKEW: { summary: { message: 'clock skew detected on mon.b' } },
      },
    })
    expect(reasons).toHaveLength(2)
    expect(reasons.join(' ')).toContain('no replicas')
  })

  it('rend une liste vide sur HEALTH_OK', () => {
    expect(cephWarningReasons({ health: 'HEALTH_OK', checks: {} })).toEqual([])
  })

  it('ne masque pas un WARN dont les motifs manquent', () => {
    // A WARN with no checks is still a WARN. Returning [] here would let
    // the screen render "healthy" for a degraded cluster.
    const reasons = cephWarningReasons({ health: 'HEALTH_WARN', checks: {} })
    expect(reasons).toHaveLength(1)
    expect(reasons[0].toLowerCase()).toContain('reason')
  })
})
