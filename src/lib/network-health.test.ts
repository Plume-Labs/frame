import { describe, it, expect } from 'vitest'
import { DEFAULT_NETWORK_EXPECTATIONS, expectationsProblem, lacpLine, memberLine, networkNotes, speedLabel, toExpectations } from './network-health'
import { translate, type Message } from '@/i18n'
import { toMachine, type NetworkBond } from './machines'

const en = (m: Message | null) => (m ? translate('en', m.key, m.params) : '')

const bond = (over: Partial<NetworkBond> = {}): NetworkBond => ({
  name: 'bond0', mode: '802.3ad', miiStatus: 'up', aggregatorID: 1, portsInAggregator: 2, partnerMAC: '02:00:00:00:00:01',
  members: [{ name: 'eno1', miiStatus: 'up', aggregatorID: 1 }, { name: 'eno2', miiStatus: 'up', aggregatorID: 1 }],
  ...over,
})

describe('memberLine', () => {
  it('montre un membre sain', () => {
    expect(memberLine(bond())[0]).toMatchObject({ tone: 'accent', status: 'up', notes: [] })
  })
  it('signale un membre tombé et ses pertes de lien', () => {
    const b = bond({ members: [{ name: 'eno1', miiStatus: 'down', linkFailures: 3, aggregatorID: 1 }] })
    expect(memberLine(b)[0]).toMatchObject({ tone: 'destructive', status: 'down' })
    expect(memberLine(b)[0].notes.map(en)).toEqual(['3 link failures'])
  })
  it('signale un membre up hors de l’agrégat', () => {
    const b = bond({ members: [{ name: 'eno1', miiStatus: 'up', aggregatorID: 2 }] })
    expect(memberLine(b)[0].tone).toBe('destructive')
    expect(memberLine(b)[0].notes.map(en)[0]).toContain('aggregator 2')
  })
  it('ne colore pas en rouge un état non lu', () => {
    expect(memberLine(bond({ members: [{ name: 'eno1' }] }))[0]).toMatchObject({ tone: 'muted', status: '?', notes: [] })
  })
  it('ne compare pas à un agrégateur du bond non lu', () => {
    const b = bond({ aggregatorID: undefined, members: [{ name: 'eno1', miiStatus: 'up', aggregatorID: 2 }] })
    expect(memberLine(b)[0].tone).toBe('accent')
  })
  it('ignore les agrégateurs hors LACP', () => {
    const b = bond({ mode: 'active-backup', members: [{ name: 'eno1', miiStatus: 'up', aggregatorID: 2 }] })
    expect(memberLine(b)[0].tone).toBe('accent')
  })
})

describe('lacpLine', () => {
  it('dit quand le switch ne répond pas', () => {
    expect(en(lacpLine(bond({ partnerMAC: '00:00:00:00:00:00' })))).toContain('no LACP partner')
  })
  it('distingue un partenaire non lu d’un partenaire absent', () => {
    expect(en(lacpLine(bond({ partnerMAC: undefined })))).toContain('not read')
  })
  it('ne compte pas des ports non lus', () => {
    expect(en(lacpLine(bond({ portsInAggregator: undefined })))).toContain('? port(s)')
  })
  it('se tait hors LACP', () => {
    expect(lacpLine(bond({ mode: 'balance-rr' }))).toBeNull()
  })
})

describe('networkNotes', () => {
  const now = Date.parse('2026-11-06T12:00:00Z')
  it('dit qu’aucun relevé n’existe', () => {
    expect(en(networkNotes(null, now)[0])).toContain('not reported')
  })
  it('donne la lecture ratée sans rien d’autre', () => {
    expect(networkNotes({ error: 'EACCES' }, now).map(en)).toEqual(['Could not be read: EACCES'])
  })
  it('date un agent muet et compte ce qui manque', () => {
    const notes = networkNotes({ observedAt: '2026-11-06T11:40:00Z', interfacesOmitted: 2 }, now).map(en)
    expect(notes[0]).toContain('20 min')
    expect(notes[1]).toContain('2 port(s)')
  })
})

describe('networkNotes des bonds', () => {
  const now = Date.parse('2026-11-06T12:00:00Z')
  const seen = '2026-11-06T11:59:00Z'
  it('compte les bonds coupés avec les ports', () => {
    expect(en(networkNotes({ observedAt: seen, bondsOmitted: 2 }, now)[0])).toContain('2 port(s)')
  })
  it('dit les membres coupés ou non lus', () => {
    const notes = networkNotes({ observedAt: seen, bonds: [bond({ membersOmitted: 3 }), bond({ name: 'bond1', membersUnread: true })] }, now).map(en)
    expect(notes).toEqual([expect.stringContaining('bond0: 3 member(s)'), expect.stringContaining('bond1: its members')])
  })
})

describe('expectationsProblem', () => {
  const ok = { ...DEFAULT_NETWORK_EXPECTATIONS, links: [{ node: 'a', interface: 'bond0', members: 2 }] }
  it('accepte une attente valide', () => {
    expect(expectationsProblem(ok)).toBeNull()
  })
  it('refuse ce que le CRD refuserait', () => {
    expect(expectationsProblem({ ...ok, minLinkSpeedMbps: 0 })).not.toBeNull()
    expect(en(expectationsProblem({ ...ok, linkSaturatedPercent: 101 }))).toMatch(/Saturation/)
    expect(en(expectationsProblem({ ...ok, linkErrorsPerMinute: 0 }))).toMatch(/Errors/)
    expect(expectationsProblem({ ...ok, links: [{ node: '', interface: 'x' }] })).not.toBeNull()
    expect(expectationsProblem({ ...ok, links: [{ node: 'a', interface: 'x', members: 17 }] })).not.toBeNull()
    expect(expectationsProblem({ ...ok, links: [{ node: 'a', interface: 'a-name-of-16-char' }] })).not.toBeNull()
    expect(expectationsProblem({ ...ok, minLinkSpeedMbps: 1.5 })).not.toBeNull()
    expect(expectationsProblem({ ...ok, links: [{ node: 'a', interface: 'x', speedMbps: 0 }] })).not.toBeNull()
    const many = Array.from({ length: 65 }, (_, i) => ({ node: 'a', interface: `p${i}` }))
    expect(en(expectationsProblem({ ...ok, links: many }))).toContain('64')
    expect(expectationsProblem({ ...ok, links: many.slice(0, 64) })).toBeNull()
    const twice = { node: 'a', interface: 'x' }
    expect(en(expectationsProblem({ ...ok, links: [twice, { ...twice }] }))).toContain('twice')
  })
})

describe('divers', () => {
  it('garde le défaut du champ absent', () => {
    expect(toExpectations({ spec: { links: [] } }).minLinkSpeedMbps).toBe(1000)
  })
  it('écrit les vitesses', () => {
    expect([speedLabel(100), speedLabel(10000), speedLabel(undefined)]).toEqual(['100 Mb/s', '10 Gb/s', '—'])
  })
  it('toMachine porte le relevé', () => {
    const m = toMachine({ metadata: { name: 'm', namespace: 'd' }, status: { network: { interfaces: [{ name: 'eno1' }] } } })
    expect(m.network?.interfaces?.[0].name).toBe('eno1')
  })
})
