// Pure logic behind the hardware console screen: inventory, sensors, event
// log, staleness and severity judgments. No network, no DOM — this is the
// layer vitest can reach (only src/**/*.test.ts runs; the .tsx screen cannot
// be unit-tested here, so every decision belongs in this file).
//
// The BMC replays cached sensor readings as if they were live: on the real
// ML350 Gen9, CPU1 reported 40 C with Status.State "Enabled" identically
// whether the machine was powered off, mid-POST, or POST-complete, twenty
// minutes after being switched off. Nothing in a reading distinguishes a
// measurement from a memory, so the operator clears status.sensors to nil
// whenever the machine is not On with POST finished, rather than storing a
// value it cannot vouch for. The consequence here: a machine with no sensors
// is the normal case, not an error, and the screen must be able to say why.

import type { NamespaceNode } from './workloads'
import type { DiskDivergence } from './storage'

export type SensorSeverity = 'ok' | 'warning' | 'critical' | 'unknown'

export interface ProcessorInfo {
  socket: string
  model: string
  cores: number
  threads: number
}

export interface MemoryModuleInfo {
  slot: string
  sizeMiB: number
  type: string
  manufacturer: string
}

export interface DriveInfo {
  name: string
  model: string
  sizeGB: number
  protocol: string
  health: string
  // The four fields below back the two-source disk inventory
  // (`api/frame/v1beta1/framemachine_types.go`'s `DriveInfo`): the BMC's
  // half of a picture whose other half is `Machine.storage.observed`, and
  // the two are joined on `serialNumber` alone and never merged on screen
  // (see `MachineDisksPanel.tsx` / `docs/superpowers/specs/2026-09-13-storage-design.md` §3).
  serialNumber: string
  location: string
  mediaType: string
  statusReasons: string[]
}

/**
 * One whole disk as the node's own kernel reports it — the second of the
 * two storage sources. Never merged with `DriveInfo`: the gap between them
 * is the datum.
 */
export interface ObservedDisk {
  path: string
  serialNumber: string
  sizeGB: number
  occupancy: string
}

/**
 * The node-side half of the storage picture plus the computed gap against
 * `MachineInventory.drives`. `null` on a `Machine` means the agent has
 * never reported — distinct from an empty `observed` list, which
 * `observedAt` is what lets a screen tell apart.
 */
export interface MachineStorage {
  observed: ObservedDisk[]
  divergences: DiskDivergence[]
  observedAt: string | null
}

export interface NetworkAdapterInfo {
  name: string
  mac: string
  status: string
}

export interface MachineInventory {
  manufacturer: string
  model: string
  serialNumber: string
  biosVersion: string
  bmcFirmware: string
  processors: ProcessorInfo[]
  memoryModules: MemoryModuleInfo[]
  totalMemoryGiB: number
  drives: DriveInfo[]
  networkAdapters: NetworkAdapterInfo[]
}

export interface TemperatureReading {
  name: string
  celsius: number
  upperCritical: number | null
  health: string
}

export interface FanReading {
  name: string
  reading: number
  units: string
  health: string
}

export interface PowerSupplyReading {
  name: string
  health: string
  state: string
  lastPowerOutputWatts: number
}

export interface MachineSensors {
  temperatures: TemperatureReading[]
  fans: FanReading[]
  powerSupplies: PowerSupplyReading[]
  powerConsumedWatts: number
}

export interface MachineEvent {
  id: string
  severity: string
  message: string
  created: string
}

export interface Machine {
  name: string
  namespace: string
  address: string
  nodeRef: string
  powerState: string
  postState: string
  indicatorLED: string
  reachable: boolean
  reachableReason: string
  reachableMessage: string
  lastProbeAt: string | null
  sensorsValidAt: string | null
  inventory: MachineInventory | null
  storage: MachineStorage | null
  sensors: MachineSensors | null
  eventLog: MachineEvent[]
  eventLogCounts: Record<string, number>
  eventLogTotal: number
  // True when the BMC's log spans more than one page and the redfish client
  // could not confirm it reached the true last page (see
  // internal/redfish.Snapshot.LogPossiblyStale). eventLog/eventLogCounts
  // above still populate from whatever page was actually read, but that
  // page may be the machine's oldest entries rather than its newest.
  eventLogPossiblyStale: boolean
  lastPowerAction: string
  lastPowerActionAt: string | null
  lastPowerActionError: string
}

interface MachineCondition {
  type: string
  status: string
  reason?: string
  message?: string
}

// Everything below `metadata` is optional: a machine can be registered and
// never successfully probed, in which case `status` (and most of `spec`) is
// absent rather than zero-valued.
export interface MachineCR {
  metadata: { name: string; namespace: string }
  spec?: {
    bmc?: { address?: string }
    nodeRef?: string
  }
  status?: {
    powerState?: string
    postState?: string
    indicatorLED?: string
    inventory?: MachineInventory
    storage?: { observed?: ObservedDisk[]; divergences?: DiskDivergence[]; observedAt?: string }
    sensors?: MachineSensors
    sensorsValidAt?: string
    eventLog?: MachineEvent[]
    eventLogCounts?: Record<string, number>
    eventLogTotal?: number
    eventLogPossiblyStale?: boolean
    lastProbeAt?: string
    lastPowerAction?: string
    lastPowerActionAt?: string
    lastPowerActionError?: string
    conditions?: MachineCondition[]
  }
}

export function toMachine(cr: MachineCR): Machine {
  const status = cr.status ?? {}
  const conditions = status.conditions ?? []
  const reachableCondition = conditions.find((c) => c.type === 'Reachable')
  const reachable = reachableCondition?.status === 'True'

  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    address: cr.spec?.bmc?.address ?? '',
    nodeRef: cr.spec?.nodeRef ?? '',
    powerState: status.powerState ?? '',
    postState: status.postState ?? '',
    indicatorLED: status.indicatorLED ?? '',
    reachable,
    reachableReason: reachableCondition?.reason ?? '',
    reachableMessage: reachableCondition?.message ?? '',
    lastProbeAt: status.lastProbeAt ?? null,
    sensorsValidAt: status.sensorsValidAt ?? null,
    inventory: status.inventory ?? null,
    storage: status.storage
      ? {
          observed: status.storage.observed ?? [],
          divergences: status.storage.divergences ?? [],
          observedAt: status.storage.observedAt ?? null,
        }
      : null,
    sensors: status.sensors ?? null,
    eventLog: status.eventLog ?? [],
    eventLogCounts: status.eventLogCounts ?? {},
    eventLogTotal: status.eventLogTotal ?? 0,
    eventLogPossiblyStale: status.eventLogPossiblyStale ?? false,
    lastPowerAction: status.lastPowerAction ?? '',
    lastPowerActionAt: status.lastPowerActionAt ?? null,
    lastPowerActionError: status.lastPowerActionError ?? '',
  }
}

export function temperatureSeverity(t: {
  celsius: number
  upperCritical?: number | null
  health?: string
}): SensorSeverity {
  if (t.upperCritical != null) {
    if (t.celsius >= t.upperCritical) return 'critical'
    if (t.celsius >= t.upperCritical - 5) return 'warning'
    return 'ok'
  }
  switch (t.health?.toLowerCase()) {
    case 'ok':
      return 'ok'
    case 'warning':
      return 'warning'
    case 'critical':
      return 'critical'
    default:
      return 'unknown'
  }
}

// Caller-facing helper: pairs each temperature reading with its severity,
// and is the only place production code should call temperatureSeverity for
// a machine's own sensors. A machine whose sensors are absent (powered off,
// mid-POST, unreachable, never read — see sensorAvailability) has nothing to
// grade, so this returns an empty list rather than the caller reaching into
// `machine.sensors!.temperatures` and throwing on null.
export function machineTemperatureReadings(
  machine: Machine,
): Array<{ reading: TemperatureReading; severity: SensorSeverity }> {
  if (!machine.sensors) return []
  return machine.sensors.temperatures.map((reading) => ({
    reading,
    severity: temperatureSeverity(reading),
  }))
}

export function eventSeverity(raw: string): SensorSeverity {
  switch (raw.toLowerCase()) {
    case 'ok':
      return 'ok'
    case 'warning':
      return 'warning'
    case 'critical':
      return 'critical'
    default:
      return 'unknown'
  }
}

const STALE_AFTER_MS = 150_000 // two polling intervals plus a half

// Shared wording for "there is nothing here because it has never been
// read" — `stalenessLabel` uses it for a null timestamp; screens reuse the
// same constant for a structurally different absence (e.g. `inventory ===
// null`) that means the same thing to an operator, so the phrasing cannot
// drift between the two independently of each other.
export const NEVER_READ_LABEL = 'jamais relevé'

// `at` is whichever timestamp is under judgment — a machine's own liveness
// is `lastProbeAt`, but a sensor reading's age is `sensorsValidAt`, and the
// two diverge: a probe can succeed against a powered-off machine (advancing
// lastProbeAt) while the sensors it reports describe an earlier moment
// (sensorsValidAt stays put). Callers pick which one they mean; this
// function does not reach into a Machine to guess.
export function isStale(at: string | null, now: Date): boolean {
  if (at === null) return true
  return now.getTime() - new Date(at).getTime() > STALE_AFTER_MS
}

export function stalenessLabel(at: string | null, now: Date): string {
  if (at === null) return NEVER_READ_LABEL
  const seconds = Math.floor((now.getTime() - new Date(at).getTime()) / 1000)
  if (seconds < 60) return `il y a ${seconds} s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `il y a ${minutes} min`
  const hours = Math.floor(minutes / 60)
  return `il y a ${hours} h`
}

export function powerActionLabel(action: string, machineName: string): string {
  return `power: ${action} ${machineName}`.slice(0, 200)
}

/**
 * The seven `PowerAction` values the CRD accepts
 * (`api/frame/v1beta1/framemachine_types.go`), all offered by the console —
 * the LED toggle is one button over two of them (`IndicatorLedOn`/
 * `IndicatorLedOff`), chosen from the machine's current `indicatorLED`.
 * `Nmi` and `PushPowerButton` are not members of this enum at all: they are
 * Redfish `ResetType` values the operator's `resolveResetType`
 * (`internal/redfish/client.go`) uses internally to talk to hardware whose
 * `Actions#ComputerSystem.Reset` doesn't list `GracefulShutdown` — a detail
 * of the wire protocol, never a choice offered here.
 */
export type PowerAction =
  | 'On'
  | 'GracefulShutdown'
  | 'ForceOff'
  | 'ForceRestart'
  | 'ClearSEL'
  | 'IndicatorLedOn'
  | 'IndicatorLedOff'

/**
 * Actions that end or interrupt whatever the machine is currently doing —
 * the ones where a cluster node's workload is a consequence, not a detail,
 * and the confirmation dialog owes the person clicking a count of what runs
 * there. `On`, the LED toggle and clearing the log touch nothing that is
 * currently running.
 */
export const DISRUPTIVE_POWER_ACTIONS: ReadonlySet<PowerAction> = new Set<PowerAction>([
  'GracefulShutdown',
  'ForceOff',
  'ForceRestart',
])

/**
 * How many pods `tree` (the same shape `WorkloadClient.tree()` returns, and
 * `WorkloadsView` already renders) places on `nodeName` — every pod under a
 * tracked controller plus every bare pod, across every namespace.
 *
 * This is the one sentence an administrator reads before switching off a
 * machine that may be carrying production, so it lives here rather than
 * inside the dialog's fetch callback: a rename of `nodeName` or a pod
 * landing in a bucket this function forgets to sum would otherwise fail
 * silently — the dialog would just report a smaller number, with nothing
 * turning red — whereas here it is one function vitest can pin against a
 * fixture that covers every bucket `NamespaceNode` has.
 */
export function countPodsOnNode(tree: NamespaceNode[], nodeName: string): number {
  return tree.reduce((total, ns) => {
    const controllerPods = ns.controllers.reduce(
      (n, c) => n + c.pods.filter((p) => p.nodeName === nodeName).length,
      0,
    )
    const barePods = ns.barePods.filter((p) => p.nodeName === nodeName).length
    return total + controllerPods + barePods
  }, 0)
}

// What the screen can say about an absent sensor set, derived only from
// signals the operator actually reports — never guessed.
//
// 'last-known' exists because a failed probe does NOT clear status: the
// controller's Reconcile deliberately keeps the last reading beside its
// age rather than blanking the panel on a transient BMC hiccup (see
// applySnapshot's doc comment in framemachine_controller.go). An
// unreachable machine can therefore still be carrying a perfectly good —
// merely aging — sensor set, and 'unreachable' alone would hide it from
// any caller that gates rendering on this function.
export type SensorAvailability =
  | { kind: 'available' }
  | { kind: 'powered-off' }
  | { kind: 'in-post' }
  | { kind: 'last-known' } // BMC not answering now, but a valid reading survives
  | { kind: 'unreachable' } // BMC not answering, and nothing survives either
  | { kind: 'never-read' }

export function sensorAvailability(machine: Machine): SensorAvailability {
  // sensorsValidAt is the one signal that means what its name says: it is
  // set exactly when a probe last found the sensors trustworthy (see
  // applySnapshot), and never cleared afterwards even if a later probe
  // finds them untrustworthy again. Null here means no probe, ever, has
  // found this machine in a state where its sensors meant anything — which
  // is a stronger and more useful fact than "it happens to be off right
  // now", so it is checked first regardless of the machine's current state.
  if (machine.sensorsValidAt === null) return { kind: 'never-read' }

  // Mirrors internal/redfish/client.go's SensorsTrustworthy as an exclusion,
  // not an inclusion: PowerOff and InPost are the two states known to still
  // serve stale data while powered on/off; everything else is treated as
  // fine so an unrecognised future PostState does not blind the console
  // forever (the captured machine never booted an OS, so no "running"
  // PostState has ever been observed).
  if (machine.powerState !== 'On') return { kind: 'powered-off' }
  // Only postState === 'InPost' is checked here, deliberately not
  // 'PowerOff' too: the branch above already returns 'powered-off' for
  // every machine whose powerState isn't 'On', so postState === 'PowerOff'
  // can only be seen here in the contradictory case where powerState says
  // On — and rendering that as "in-post" (Démarrage en cours) would assert
  // the opposite of what postState itself is saying.
  if (machine.postState === 'InPost') {
    return { kind: 'in-post' }
  }

  if (!machine.reachable) {
    return machine.sensors ? { kind: 'last-known' } : { kind: 'unreachable' }
  }

  return { kind: 'available' }
}
