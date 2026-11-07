// The Links tab's reading of `status.network` and of the NetworkHealthPolicy.
// Pure. As on the Health tab, the verdict is the controller's: the rows only say
// what the agent read.

import type { MachineNetwork, NetworkBond, NetworkInterface } from './machines'
import type { Tone } from './thresholds'
import { agentFreshness } from './disk-inventory'

export const NETWORK_HEALTH_POLICY_NAME = 'default'

export interface ExpectedLink {
  node: string
  interface: string
  speedMbps?: number
  members?: number
}

export interface NetworkExpectations {
  minLinkSpeedMbps: number
  linkSaturatedPercent: number
  linkErrorsPerMinute: number
  links: ExpectedLink[]
}

/** Mirrors the `Default…` constants in networkhealthpolicy_types.go. */
export const DEFAULT_NETWORK_EXPECTATIONS: NetworkExpectations = {
  minLinkSpeedMbps: 1000,
  linkSaturatedPercent: 80,
  linkErrorsPerMinute: 1,
  links: [],
}

export interface NetworkHealthPolicyCR {
  spec?: Partial<NetworkExpectations>
}

export function toExpectations(cr: NetworkHealthPolicyCR): NetworkExpectations {
  return { ...DEFAULT_NETWORK_EXPECTATIONS, ...cr.spec }
}

/** The CRD's bounds; an empty optional stays absent rather than zero. */
export function expectationsProblem(e: NetworkExpectations): string | null {
  if (!Number.isInteger(e.minLinkSpeedMbps) || e.minLinkSpeedMbps < 1) return 'Minimum speed: a whole number of Mb/s.'
  if (!Number.isInteger(e.linkSaturatedPercent) || e.linkSaturatedPercent < 1 || e.linkSaturatedPercent > 100) {
    return 'Saturation: a percentage from 1 to 100.'
  }
  if (!Number.isInteger(e.linkErrorsPerMinute) || e.linkErrorsPerMinute < 1) return 'Errors: a whole number a minute.'
  if (e.links.length > 64) return 'At most 64 expected links.'
  for (const l of e.links) {
    if (!l.node || !l.interface) return 'Each expected link needs a node and an interface.'
    if (e.links.some((o) => o !== l && o.node === l.node && o.interface === l.interface)) return `${l.interface}: named twice on ${l.node}.`
    if (l.interface.length > 15) return `${l.interface}: an interface name is 15 characters at most.`
    if (l.speedMbps !== undefined && (!Number.isInteger(l.speedMbps) || l.speedMbps < 1)) return `${l.interface}: speed must be a whole number of Mb/s.`
    if (l.members !== undefined && (!Number.isInteger(l.members) || l.members < 1 || l.members > 16)) return `${l.interface}: members go from 1 to 16.`
  }
  return null
}

export function speedLabel(mbps: number | undefined): string {
  if (mbps === undefined) return '—'
  return mbps >= 1000 ? `${mbps / 1000} Gb/s` : `${mbps} Mb/s`
}

export function portTone(i: NetworkInterface): Tone {
  return i.operState === 'up' ? 'accent' : 'muted'
}

export function portErrors(i: NetworkInterface): number {
  return (i.rxErrors ?? 0) + (i.txErrors ?? 0)
}

export function portDrops(i: NetworkInterface): number {
  return (i.rxDropped ?? 0) + (i.txDropped ?? 0)
}

/** The bond's own view of a member, in words. */
export function memberLine(b: NetworkBond): { name: string; tone: Tone; detail: string }[] {
  return (b.members ?? []).map((m) => {
    const outOfAggregate = b.mode === '802.3ad' && !!b.aggregatorID && !!m.aggregatorID && m.aggregatorID !== b.aggregatorID
    const up = m.miiStatus === 'up'
    return {
      name: m.name,
      tone: outOfAggregate || (m.miiStatus && !up) ? 'destructive' : up ? 'accent' : 'muted',
      detail: [
        m.miiStatus ?? '?',
        outOfAggregate ? `aggregator ${m.aggregatorID}, the bond uses ${b.aggregatorID}` : '',
        m.linkFailures ? `${m.linkFailures} link failures` : '',
      ].filter(Boolean).join(' · '),
    }
  })
}

/** LACP's partner: all zeros means the switch side answers nothing. */
export function lacpLine(b: NetworkBond): string {
  if (b.mode !== '802.3ad') return ''
  const partner = !b.partnerMAC ? 'LACP partner not read' : /^[0:]+$/.test(b.partnerMAC) ? 'no LACP partner' : `partner ${b.partnerMAC}`
  return `${partner} · ${b.portsInAggregator ?? '?'} port(s) in aggregator ${b.aggregatorID ?? '?'}`
}

/** What the rows cannot show: a read that failed, its age, what was cut. */
export function networkNotes(n: MachineNetwork | null, now: number): string[] {
  if (!n) return ['The agent has not reported this node’s links.']
  if (n.error) return [`Could not be read: ${n.error}`]
  const notes: string[] = []
  const { ageS, stale } = agentFreshness(n.observedAt ?? null, now)
  if (stale) notes.push(ageS === null ? 'The agent has not dated this reading.' : `Reading ${Math.round(ageS / 60)} min old: the agent is silent.`)
  const cut = (n.interfacesOmitted ?? 0) + (n.bondsOmitted ?? 0)
  if (cut > 0) notes.push(`${cut} port(s) or bond(s) past the agent's list limit are not shown.`)
  for (const b of n.bonds ?? []) {
    if (b.membersUnread) notes.push(`${b.name}: its members could not be read.`)
    if (b.membersOmitted) notes.push(`${b.name}: ${b.membersOmitted} member(s) past the agent's list limit are not shown.`)
  }
  return notes
}

