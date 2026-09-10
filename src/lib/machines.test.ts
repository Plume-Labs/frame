import { describe, expect, it } from 'vitest'
import {
  eventSeverity,
  isStale,
  machineTemperatureReadings,
  powerActionLabel,
  sensorAvailability,
  stalenessLabel,
  temperatureSeverity,
  toMachine,
  type Machine,
  type MachineCR,
} from './machines'

describe('temperatureSeverity', () => {
  it('is critical at or above the reported upper critical threshold', () => {
    expect(temperatureSeverity({ celsius: 70, upperCritical: 70 })).toBe('critical')
    expect(temperatureSeverity({ celsius: 71, upperCritical: 70 })).toBe('critical')
  })

  it('warns within five degrees of the threshold, so the screen is useful before the alarm', () => {
    expect(temperatureSeverity({ celsius: 66, upperCritical: 70 })).toBe('warning')
    expect(temperatureSeverity({ celsius: 65, upperCritical: 70 })).toBe('warning')
  })

  it('is ok further below', () => {
    expect(temperatureSeverity({ celsius: 64, upperCritical: 70 })).toBe('ok')
    expect(temperatureSeverity({ celsius: 22, upperCritical: 42 })).toBe('ok')
  })

  // iLO4 omits the threshold on most DIMM sensors. Inventing one would be a
  // guess rendered as a fact, so the reported health is the fallback and
  // absence of both is 'unknown', not 'ok'.
  it('falls back to the reported health when there is no threshold', () => {
    expect(temperatureSeverity({ celsius: 31, health: 'OK' })).toBe('ok')
    expect(temperatureSeverity({ celsius: 31, health: 'Warning' })).toBe('warning')
    expect(temperatureSeverity({ celsius: 31, health: 'Critical' })).toBe('critical')
    expect(temperatureSeverity({ celsius: 31 })).toBe('unknown')
  })
})

// Pins the interaction between temperatureSeverity and a Machine whose
// sensors are absent: temperatureSeverity itself takes a bare reading and
// has no way to refuse a null machine.sensors, so the guard has to live one
// level up, in the caller-facing helper the screen actually calls. This
// test proves that helper — not temperatureSeverity — is where "no sensors"
// is handled, and that it never reaches into a null sensors object.
describe('machineTemperatureReadings', () => {
  const base: Machine = {
    name: 'ml350-g9',
    namespace: 'default',
    address: '192.168.2.60',
    nodeRef: '',
    powerState: 'Off',
    postState: 'PowerOff',
    indicatorLED: '',
    reachable: true,
    reachableReason: 'Probed',
    reachableMessage: '',
    lastProbeAt: '2026-09-10T11:59:30Z',
    sensorsValidAt: null,
    inventory: null,
    sensors: null,
    eventLog: [],
    eventLogCounts: {},
    eventLogTotal: 0,
    lastPowerAction: '',
    lastPowerActionAt: null,
    lastPowerActionError: '',
  }

  it('returns an empty list rather than throwing when sensors is null', () => {
    expect(machineTemperatureReadings(base)).toEqual([])
  })

  it('pairs each reading with its severity when sensors are present', () => {
    const withSensors: Machine = {
      ...base,
      powerState: 'On',
      postState: 'InPostDiscoveryComplete',
      sensors: {
        temperatures: [{ name: 'CPU1', celsius: 40, upperCritical: 70, health: 'OK' }],
        fans: [],
        powerSupplies: [],
        powerConsumedWatts: 120,
      },
    }
    expect(machineTemperatureReadings(withSensors)).toEqual([
      {
        reading: { name: 'CPU1', celsius: 40, upperCritical: 70, health: 'OK' },
        severity: 'ok',
      },
    ])
  })
})

describe('eventSeverity', () => {
  it('maps the vocabulary the BMC actually uses', () => {
    expect(eventSeverity('OK')).toBe('ok')
    expect(eventSeverity('Warning')).toBe('warning')
    expect(eventSeverity('Critical')).toBe('critical')
  })

  it('is case-insensitive, because firmware revisions disagree on it', () => {
    expect(eventSeverity('critical')).toBe('critical')
    expect(eventSeverity('WARNING')).toBe('warning')
  })

  it('does not silently downgrade a severity it has never seen', () => {
    expect(eventSeverity('Fatal')).toBe('unknown')
    expect(eventSeverity('')).toBe('unknown')
  })
})

describe('staleness', () => {
  const now = new Date('2026-09-10T12:00:00Z')

  it('is not stale within two polling intervals', () => {
    expect(isStale('2026-09-10T11:59:30Z', now)).toBe(false)
    expect(isStale('2026-09-10T11:58:05Z', now)).toBe(false)
  })

  it('is stale beyond them', () => {
    expect(isStale('2026-09-10T11:57:00Z', now)).toBe(true)
  })

  // A machine that has never been probed has no reading to be stale about,
  // and rendering "0 s ago" would be a lie about a value that does not exist.
  it('treats never-probed as stale', () => {
    expect(isStale(null, now)).toBe(true)
    expect(stalenessLabel(null, now)).toBe('jamais relevé')
  })

  it('says how old the reading is in words', () => {
    expect(stalenessLabel('2026-09-10T11:59:30Z', now)).toBe('il y a 30 s')
    expect(stalenessLabel('2026-09-10T11:46:00Z', now)).toBe('il y a 14 min')
    expect(stalenessLabel('2026-09-10T09:00:00Z', now)).toBe('il y a 3 h')
  })

  // The functions take the timestamp they are judging rather than reaching
  // into a Machine for lastProbeAt, so both call sites — the machine's own
  // liveness and the sensors' age — go through the same code with different
  // arguments. A machine can be probed successfully (lastProbeAt fresh)
  // while off, whose sensors describe a moment a week earlier
  // (sensorsValidAt ancient): conflating the two fields would report fresh
  // sensors on a machine that has been off for a week.
  it('discriminates sensor staleness from machine staleness on the same machine', () => {
    const lastProbeAt = '2026-09-10T11:59:55Z' // 5 s ago: machine is live
    const sensorsValidAt = '2026-09-03T12:00:00Z' // a week ago: sensors are not

    expect(isStale(lastProbeAt, now)).toBe(false)
    expect(isStale(sensorsValidAt, now)).toBe(true)
    expect(stalenessLabel(lastProbeAt, now)).toBe('il y a 5 s')
    expect(stalenessLabel(sensorsValidAt, now)).not.toBe(stalenessLabel(lastProbeAt, now))
  })
})

describe('powerActionLabel', () => {
  it('names the action and the machine, in that order', () => {
    expect(powerActionLabel('GracefulShutdown', 'ml350-g9')).toBe(
      'power: GracefulShutdown ml350-g9',
    )
  })

  // FrameTaskSpec.Action caps at 200 characters and an overflow does not fail
  // the write — it drops the audit record. The machine name is what gets cut,
  // because the action is the part that cannot be reconstructed.
  it('truncates to 200 characters rather than losing the record', () => {
    const long = 'm'.repeat(400)
    const label = powerActionLabel('ForceOff', long)
    expect(label.length).toBe(200)
    expect(label.startsWith('power: ForceOff ')).toBe(true)
  })
})

describe('toMachine', () => {
  it('reads the Reachable condition into three flat fields', () => {
    const m = toMachine({
      metadata: { name: 'ml350-g9', namespace: 'default' },
      spec: { bmc: { address: '192.168.2.60' }, nodeRef: 'w2' },
      status: {
        powerState: 'On',
        lastProbeAt: '2026-09-10T11:59:30Z',
        conditions: [
          {
            type: 'Reachable',
            status: 'False',
            reason: 'TLSError',
            message: 'x509: certificate signed by unknown authority',
          },
        ],
      },
    })
    expect(m.reachable).toBe(false)
    expect(m.reachableReason).toBe('TLSError')
    expect(m.reachableMessage).toContain('unknown authority')
    expect(m.address).toBe('192.168.2.60')
    expect(m.nodeRef).toBe('w2')
  })

  it('survives a machine that has never been probed', () => {
    const m = toMachine({
      metadata: { name: 'fresh', namespace: 'default' },
      spec: { bmc: { address: '192.168.2.61' } },
    })
    expect(m.reachable).toBe(false)
    expect(m.lastProbeAt).toBeNull()
    expect(m.inventory).toBeNull()
    expect(m.sensors).toBeNull()
    expect(m.eventLog).toEqual([])
    expect(m.eventLogTotal).toBe(0)
    expect(m.postState).toBe('')
    expect(m.sensorsValidAt).toBeNull()
    expect(m.lastPowerActionError).toBe('')
  })

  // The three fields the amended status carries: postState (POST progress
  // as the BMC reports it), sensorsValidAt (distinct from lastProbeAt — see
  // the staleness tests above), and lastPowerActionError (cleared on a
  // successful action, so its presence alone means the last one failed).
  it('maps postState, sensorsValidAt and lastPowerActionError', () => {
    const m = toMachine({
      metadata: { name: 'ml350-g9', namespace: 'default' },
      spec: { bmc: { address: '192.168.2.60' } },
      status: {
        powerState: 'On',
        postState: 'InPostDiscoveryComplete',
        sensorsValidAt: '2026-09-10T11:59:30Z',
        lastPowerAction: 'ForceOff',
        lastPowerActionError: 'context deadline exceeded',
        conditions: [{ type: 'Reachable', status: 'True', reason: 'Probed' }],
      },
    })
    expect(m.postState).toBe('InPostDiscoveryComplete')
    expect(m.sensorsValidAt).toBe('2026-09-10T11:59:30Z')
    expect(m.lastPowerActionError).toBe('context deadline exceeded')
  })
})

describe('sensorAvailability', () => {
  const cr = (status: NonNullable<MachineCR['status']>): MachineCR => ({
    metadata: { name: 'ml350-g9', namespace: 'default' },
    spec: { bmc: { address: '192.168.2.60' } },
    status: { conditions: [{ type: 'Reachable', status: 'True', reason: 'Probed' }], ...status },
  })

  it('is powered-off for Off/PowerOff with no sensors', () => {
    const m = toMachine(cr({ powerState: 'Off', postState: 'PowerOff' }))
    expect(sensorAvailability(m)).toEqual({ kind: 'powered-off' })
  })

  it('is in-post for On/InPost with no sensors', () => {
    const m = toMachine(cr({ powerState: 'On', postState: 'InPost' }))
    expect(sensorAvailability(m)).toEqual({ kind: 'in-post' })
  })

  it('is available for On/InPostDiscoveryComplete with sensors present', () => {
    const m = toMachine(
      cr({
        powerState: 'On',
        postState: 'InPostDiscoveryComplete',
        sensors: {
          temperatures: [{ name: 'CPU1', celsius: 40, upperCritical: 70, health: 'OK' }],
          fans: [],
          powerSupplies: [],
          powerConsumedWatts: 120,
        },
      }),
    )
    expect(sensorAvailability(m)).toEqual({ kind: 'available' })
  })

  it('is unreachable when the Reachable condition is False, regardless of power state', () => {
    const m = toMachine({
      metadata: { name: 'ml350-g9', namespace: 'default' },
      spec: { bmc: { address: '192.168.2.60' } },
      status: {
        powerState: 'On',
        postState: 'InPostDiscoveryComplete',
        conditions: [{ type: 'Reachable', status: 'False', reason: 'Timeout' }],
      },
    })
    expect(sensorAvailability(m)).toEqual({ kind: 'unreachable' })
  })

  // On, POST finished, reachable — the one combination that would normally
  // carry sensors — yet sensors is still null and sensorsValidAt has never
  // been set. Nobody has ever seen this machine produce a valid reading,
  // which is a different fact from "it is off right now".
  it('is never-read for a reachable, POST-complete machine that has no sensors and no history', () => {
    const m = toMachine(cr({ powerState: 'On', postState: 'InPostDiscoveryComplete' }))
    expect(m.sensorsValidAt).toBeNull()
    expect(sensorAvailability(m)).toEqual({ kind: 'never-read' })
  })
})
