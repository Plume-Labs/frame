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

  // The load-bearing property is the meaning, not merely that the string
  // differs between branches: a `bmc-only` line has to say the disk is
  // seen by the BMC and not presented to the node, or an operator reading
  // it cannot tell which side is missing it.
  it('dit que le BMC voit le disque et que le noeud ne le presente pas', () => {
    const line = describeDivergence({ serialNumber: 'X', reason: 'bmc-only', detail: '' }).toLowerCase()
    expect(line).toContain('bmc')
    expect(line).toContain('not presented to the node')
  })

  it('dit que le noeud voit le disque et que le BMC ne le liste pas', () => {
    const line = describeDivergence({ serialNumber: 'X', reason: 'os-only', detail: '' }).toLowerCase()
    expect(line).toContain('node')
    expect(line).toContain("absent from the bmc")
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

  // Usable alone tells an operator how much room is left; without `used`
  // alongside it there is no way to read how close the cluster is to that
  // ceiling, which is the whole point of showing capacity at all.
  it('rend aussi l-utilise, pas seulement l-utilisable', () => {
    const line = capacityLine({ usable: '1.2Ti', used: '480Gi', raw: '3.6Ti' })
    expect(line).toContain('480Gi')
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

  // The count alone is meaningless without which class it is a count of:
  // a screen listing several entries side by side needs the class name in
  // the line itself, not just as a heading nothing here checks for.
  it('nomme la classe de stockage, pas seulement les comptes', () => {
    expect(claimGapLine('ceph-rbd', { total: 16, labelled: 0 })).toContain('ceph-rbd')
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
      details: {
        POOL_NO_REDUNDANCY: { message: '1 pool(s) have no replicas configured', severity: 'HEALTH_WARN' },
        MON_CLOCK_SKEW: { message: 'clock skew detected on mon.b', severity: 'HEALTH_WARN' },
      },
    })
    expect(reasons).toHaveLength(2)
    expect(reasons.join(' ')).toContain('no replicas')
  })

  it('rend une liste vide sur HEALTH_OK', () => {
    expect(cephWarningReasons({ health: 'HEALTH_OK', details: {} })).toEqual([])
  })

  // The guard has to key off `health` first, unconditionally — not off
  // whether `details` happens to be non-empty. A stale or lingering
  // `details` entry on an otherwise-OK cluster must not be read as a
  // warning; reordering the guard to "details non-empty implies warning"
  // passes every other test here while getting this one backwards.
  it('ignore des motifs residuels quand la sante est HEALTH_OK', () => {
    const reasons = cephWarningReasons({
      health: 'HEALTH_OK',
      details: { MON_CLOCK_SKEW: { message: 'clock skew detected on mon.b', severity: 'HEALTH_WARN' } },
    })
    expect(reasons).toEqual([])
  })

  it('ne masque pas un WARN dont les motifs manquent', () => {
    // A WARN with no details is still a WARN. Returning [] here would let
    // the screen render "healthy" for a degraded cluster.
    const reasons = cephWarningReasons({ health: 'HEALTH_WARN', details: {} })
    expect(reasons).toHaveLength(1)
    expect(reasons[0].toLowerCase()).toContain('reason')
  })
})
