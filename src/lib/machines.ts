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
  sensors: MachineSensors | null
  eventLog: MachineEvent[]
  eventLogCounts: Record<string, number>
  eventLogTotal: number
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
    sensors?: MachineSensors
    sensorsValidAt?: string
    eventLog?: MachineEvent[]
    eventLogCounts?: Record<string, number>
    eventLogTotal?: number
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
    sensors: status.sensors ?? null,
    eventLog: status.eventLog ?? [],
    eventLogCounts: status.eventLogCounts ?? {},
    eventLogTotal: status.eventLogTotal ?? 0,
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
  if (at === null) return 'jamais relevé'
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

// What the screen can say about an absent sensor set, derived only from
// signals the operator actually reports — never guessed.
export type SensorAvailability =
  | { kind: 'available' }
  | { kind: 'powered-off' }
  | { kind: 'in-post' }
  | { kind: 'unreachable' }
  | { kind: 'never-read' }

export function sensorAvailability(machine: Machine): SensorAvailability {
  if (!machine.reachable) return { kind: 'unreachable' }
  if (machine.sensors) return { kind: 'available' }
  if (machine.powerState !== 'On') return { kind: 'powered-off' }
  if (machine.postState !== 'InPostDiscoveryComplete') return { kind: 'in-post' }
  // Reachable, powered on, POST finished — the one combination in which the
  // operator would normally have written sensors. Since it did not, this
  // machine has no valid reading in its history at all: sensorsValidAt is
  // null here in every case observed on real hardware. "Nobody has ever
  // seen this machine work", as distinct from "it is off right now".
  return { kind: 'never-read' }
}
