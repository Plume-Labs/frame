// The Links tab's reading of `status.network` and of the NetworkHealthPolicy.
// Pure. As on the Health tab, the verdict is the controller's: the rows only say
// what the agent read.

import type { MachineNetwork, NetworkBond, NetworkInterface } from './machines'
import type { Tone } from './thresholds'
import type { Message } from '@/i18n'
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
export function expectationsProblem(e: NetworkExpectations): Message | null {
  if (!Number.isInteger(e.minLinkSpeedMbps) || e.minLinkSpeedMbps < 1) return { key: 'net.exp.minSpeed' }
  if (!Number.isInteger(e.linkSaturatedPercent) || e.linkSaturatedPercent < 1 || e.linkSaturatedPercent > 100) {
    return { key: 'net.exp.saturation' }
  }
  if (!Number.isInteger(e.linkErrorsPerMinute) || e.linkErrorsPerMinute < 1) return { key: 'net.exp.errors' }
  if (e.links.length > 64) return { key: 'net.exp.tooMany' }
  for (const l of e.links) {
    if (!l.node || !l.interface) return { key: 'net.exp.needsBoth' }
    if (e.links.some((o) => o !== l && o.node === l.node && o.interface === l.interface)) return { key: 'net.exp.twice', params: { name: l.interface, node: l.node } }
    if (l.interface.length > 15) return { key: 'net.exp.nameLen', params: { name: l.interface } }
    if (l.speedMbps !== undefined && (!Number.isInteger(l.speedMbps) || l.speedMbps < 1)) return { key: 'net.exp.speed', params: { name: l.interface } }
    if (l.members !== undefined && (!Number.isInteger(l.members) || l.members < 1 || l.members > 16)) return { key: 'net.exp.members', params: { name: l.interface } }
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

/**
 * The bond's own view of a member. `status` is the raw kernel word (`up`,
 * `down`, `?`); `notes` are what else is wrong, to be joined by the view.
 */
export function memberLine(b: NetworkBond): { name: string; tone: Tone; status: string; notes: Message[] }[] {
  return (b.members ?? []).map((m) => {
    const outOfAggregate = b.mode === '802.3ad' && !!b.aggregatorID && !!m.aggregatorID && m.aggregatorID !== b.aggregatorID
    const up = m.miiStatus === 'up'
    const notes: Message[] = []
    if (outOfAggregate) notes.push({ key: 'net.member.outOfAggregate', params: { member: m.aggregatorID!, bond: b.aggregatorID! } })
    if (m.linkFailures) notes.push({ key: 'net.member.failures', params: { n: m.linkFailures } })
    return {
      name: m.name,
      tone: outOfAggregate || (m.miiStatus && !up) ? 'destructive' : up ? 'accent' : 'muted',
      status: m.miiStatus ?? '?',
      notes,
    }
  })
}

/** LACP's partner: all zeros means the switch side answers nothing. */
export function lacpLine(b: NetworkBond): Message | null {
  if (b.mode !== '802.3ad') return null
  const params = { ports: b.portsInAggregator ?? '?', agg: b.aggregatorID ?? '?' }
  if (!b.partnerMAC) return { key: 'net.lacp.unread', params }
  if (/^[0:]+$/.test(b.partnerMAC)) return { key: 'net.lacp.none', params }
  return { key: 'net.lacp.partner', params: { ...params, mac: b.partnerMAC } }
}

/** What the rows cannot show: a read that failed, its age, what was cut. */
export function networkNotes(n: MachineNetwork | null, now: number): Message[] {
  if (!n) return [{ key: 'net.note.unreported' }]
  if (n.error) return [{ key: 'net.note.error', params: { error: n.error } }]
  const notes: Message[] = []
  const { ageS, stale } = agentFreshness(n.observedAt ?? null, now)
  if (stale) notes.push(ageS === null ? { key: 'net.note.undated' } : { key: 'net.note.old', params: { min: Math.round(ageS / 60) } })
  const cut = (n.interfacesOmitted ?? 0) + (n.bondsOmitted ?? 0)
  if (cut > 0) notes.push({ key: 'net.note.cut', params: { n: cut } })
  for (const b of n.bonds ?? []) {
    if (b.membersUnread) notes.push({ key: 'net.note.membersUnread', params: { bond: b.name } })
    if (b.membersOmitted) notes.push({ key: 'net.note.membersCut', params: { bond: b.name, n: b.membersOmitted } })
  }
  return notes
}
