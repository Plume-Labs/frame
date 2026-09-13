// Pure decisions behind the storage screens: how a divergence between the
// BMC's disk list and the node's own is worded, how capacity is stated
// (usable first, raw never alone), how the gap between PVC count and
// labelled PVC count is read, and what a Ceph WARN's reasons are. No
// network, no DOM — this is the layer vitest can reach (only src/**/*.test.ts
// runs; the screens themselves cannot be unit-tested here, so every
// decision an operator reads belongs in this file).

export interface DiskDivergence {
  serialNumber: string
  reason: 'bmc-only' | 'os-only' | 'mismatch'
  detail: string
}

export interface StorageCapacity {
  usable: string
  used: string
  raw: string
}

export interface ClaimCounts {
  total: number
  labelled: number
}

/**
 * A one-line reading of one divergence.
 *
 * It never uses fault language for `bmc-only`. On the machine this design
 * was written against, the BMC reports Health OK and status reasons
 * ["None"] for the very disk the kernel cannot see: the disk is masked by
 * residual logical-unit metadata, not broken, and a screen that says
 * "failed" sends someone to replace a healthy drive.
 */
export function describeDivergence(d: DiskDivergence): string {
  const where = d.detail ? ` — ${d.detail}` : ''
  switch (d.reason) {
    case 'bmc-only':
      return `${d.serialNumber}: seen by the BMC, not presented to the node${where}`
    case 'os-only':
      return `${d.serialNumber}: seen by the node, absent from the BMC's list${where}`
    default:
      return `${d.serialNumber}: the two sources disagree${where}`
  }
}

/**
 * Capacity, usable first.
 *
 * Raw never stands alone: the park's capacity incident came from sizing
 * OSDs on a raw number that replication divides by three. An entry with no
 * usable figure says so rather than fall back to the raw one.
 */
export function capacityLine(c: StorageCapacity): string {
  if (!c.usable) {
    return c.raw ? `usable unknown (raw ${c.raw})` : 'usable unknown'
  }
  const used = c.used ? `${c.used} used of ` : ''
  const raw = c.raw ? ` (raw ${c.raw})` : ''
  return `${used}${c.usable} usable${raw}`
}

/**
 * The gap between the policy and the cluster, stated as a fact rather than
 * a fault. Enforcement is opt-in per object, so many claims and no labels
 * is the normal state the day the webhook lands.
 */
export function claimGapLine(className: string, c: ClaimCounts): string {
  return `${className} — ${c.total} PVC, ${c.labelled} labelled`
}

/**
 * Rook's own shape for `status.ceph.details` off the CephCluster CR — the
 * field `cmd/main.go`'s `readCephHealth` already reads for FrameStorage's
 * `Healthy` condition. Deliberately not Ceph's own `status --format json`
 * `checks[code].summary.message` shape: that is a different source this
 * screen never reads, and R18 struck the translation between the two after
 * it shipped with no test on the transformation the cluster actually
 * exercises — the tested thing is now the thing the cluster serves,
 * carried through `ceph()` verbatim rather than remapped.
 */
export interface CephHealthPayload {
  health: string
  details: Record<string, { message?: string; severity?: string }>
}

/**
 * The reasons behind a Ceph warning, which the existing storage screen does
 * not show: it renders "WARN" and nothing else, and a degraded state with
 * no reason cannot be acted on.
 *
 * `health` is checked first and unconditionally: a non-empty `details` on
 * an otherwise-OK cluster (stale reads, a check that cleared but whose
 * entry hasn't yet) must still report no warning — reordering this guard
 * to key off `details` alone would render a healthy cluster as WARN, the
 * opposite of the other failure mode below.
 *
 * A WARN whose details are missing still returns one line. Returning an
 * empty list there would let the screen render a degraded cluster as
 * healthy — the failure mode this function exists to prevent.
 */
export function cephWarningReasons(payload: CephHealthPayload): string[] {
  if (payload.health === 'HEALTH_OK') return []

  const reasons = Object.entries(payload.details ?? {})
    .map(([code, detail]) => detail.message ?? code)
    .filter((m) => m.length > 0)

  if (reasons.length === 0) {
    return [`${payload.health} with no reason reported by the cluster`]
  }
  return reasons
}
