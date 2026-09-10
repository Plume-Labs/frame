import { describe, expect, it } from 'vitest'
import {
  countPodsOnNode,
  DISRUPTIVE_POWER_ACTIONS,
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
import type { NamespaceNode, WorkloadPod } from './workloads'

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
  const sampleSensors = {
    temperatures: [{ name: 'CPU1', celsius: 40, upperCritical: 70, health: 'OK' }],
    fans: [],
    powerSupplies: [],
    powerConsumedWatts: 120,
  }

  const reachable = (
    status: NonNullable<MachineCR['status']>,
    reachableStatus: 'True' | 'False' = 'True',
  ): MachineCR => ({
    metadata: { name: 'ml350-g9', namespace: 'default' },
    spec: { bmc: { address: '192.168.2.60' } },
    status: {
      conditions: [{ type: 'Reachable', status: reachableStatus, reason: 'Probed' }],
      ...status,
    },
  })

  // A freshly registered machine whose first-ever probe succeeded (BMC
  // answered, so reachable) and found it powered off. applySnapshot writes
  // PowerState/PostState on every successful probe but only sets
  // SensorsValidAt when SensorsTrustworthy — which requires powerState
  // 'On' — so this machine has reachable=true, powerState='Off', and
  // sensorsValidAt still null: exactly the controller-producible state for
  // "nobody has ever seen this machine work", which the amendment asked to
  // discriminate from "it is off right now". never-read is checked before
  // powered-off for this reason.
  it('is never-read for a reachable machine probed once while off, never validly read', () => {
    const m = toMachine(reachable({ powerState: 'Off', postState: 'PowerOff' }))
    expect(m.sensorsValidAt).toBeNull()
    expect(sensorAvailability(m)).toEqual({ kind: 'never-read' })
  })

  // Had a valid reading before (sensorsValidAt set), is now off: a later
  // successful probe found powerState 'Off' and, per applySnapshot's else
  // branch, cleared Sensors to nil while leaving SensorsValidAt where it
  // was. Fully controller-producible.
  it('is powered-off for Off/PowerOff with a sensor history but no current sensors', () => {
    const m = toMachine(
      reachable({
        powerState: 'Off',
        postState: 'PowerOff',
        sensorsValidAt: '2026-09-10T09:00:00Z',
      }),
    )
    expect(sensorAvailability(m)).toEqual({ kind: 'powered-off' })
  })

  it('is in-post for On/InPost with a sensor history but no current sensors', () => {
    const m = toMachine(
      reachable({
        powerState: 'On',
        postState: 'InPost',
        sensorsValidAt: '2026-09-10T09:00:00Z',
      }),
    )
    expect(sensorAvailability(m)).toEqual({ kind: 'in-post' })
  })

  it('is available for On/InPostDiscoveryComplete with sensors present', () => {
    const m = toMachine(
      reachable({
        powerState: 'On',
        postState: 'InPostDiscoveryComplete',
        sensorsValidAt: '2026-09-10T11:59:30Z',
        sensors: sampleSensors,
      }),
    )
    expect(sensorAvailability(m)).toEqual({ kind: 'available' })
  })

  // The controller's probe-failure branch does not clear status — it keeps
  // the last reading beside its age rather than blanking the panel on a
  // transient BMC hiccup. So an unreachable machine that was last seen On
  // and POST-complete still carries its last sensor set. Fully
  // controller-producible: this is the ordinary shape of "BMC stopped
  // answering ten minutes ago".
  it('is last-known when unreachable but a sensor reading survives from the last successful probe', () => {
    const m = toMachine(
      reachable(
        {
          powerState: 'On',
          postState: 'InPostDiscoveryComplete',
          sensorsValidAt: '2026-09-10T11:50:00Z',
          sensors: sampleSensors,
        },
        'False',
      ),
    )
    expect(sensorAvailability(m)).toEqual({ kind: 'last-known' })
  })

  // Exercises the branch's fallback when a caller feeds it a Machine whose
  // fields are not correlated the way applySnapshot always writes them —
  // the Machine type does not (and per the brief, should not) encode that
  // correlation itself, only toMachine's producer does. Per the traced
  // invariant (SensorsTrustworthy implies mapSensors is always non-nil),
  // the real controller cannot currently reach powerState 'On' +
  // postState 'InPostDiscoveryComplete' + sensors null in the same status;
  // this pins the function's behaviour for the wider type-level contract
  // rather than asserting the combination is controller-producible today —
  // see the report's self-review for why this is disclosed rather than
  // silently assumed.
  it('is unreachable when nothing survives at all', () => {
    const m: Machine = {
      ...toMachine(
        reachable(
          {
            powerState: 'On',
            postState: 'InPostDiscoveryComplete',
            sensorsValidAt: '2026-09-10T11:50:00Z',
          },
          'False',
        ),
      ),
      sensors: null,
    }
    expect(sensorAvailability(m)).toEqual({ kind: 'unreachable' })
  })

  // internal/redfish/client.go derives SensorsTrustworthy as an exclusion
  // (powered on AND postState not in {PowerOff, InPost}), not an inclusion
  // of the one known-good value, precisely so an unrecognised future
  // PostState does not blind the console forever. This machine reports a
  // plausible post-boot state this package has never catalogued.
  it('treats an unrecognised PostState on a powered-on machine as trustworthy, not in-post', () => {
    const m = toMachine(
      reachable({
        powerState: 'On',
        postState: 'RunningOS', // invented, plausible, never seen in captures
        sensorsValidAt: '2026-09-10T11:59:30Z',
        sensors: sampleSensors,
      }),
    )
    expect(sensorAvailability(m)).not.toEqual({ kind: 'in-post' })
    expect(sensorAvailability(m)).toEqual({ kind: 'available' })
  })

  // Minor fix: postState === 'PowerOff' can only be seen past the
  // powerState !== 'On' branch above in the contradictory case where
  // powerState says On — a state applySnapshot cannot currently produce,
  // but one this function used to render as 'in-post' ("Démarrage en
  // cours"), the opposite of what postState itself says. Only 'InPost'
  // should map to 'in-post' here.
  it('does not treat a contradictory On/PowerOff pair as in-post', () => {
    const m = toMachine(
      reachable({
        powerState: 'On',
        postState: 'PowerOff',
        sensorsValidAt: '2026-09-10T09:00:00Z',
      }),
    )
    expect(sensorAvailability(m)).not.toEqual({ kind: 'in-post' })
  })
})

describe('DISRUPTIVE_POWER_ACTIONS', () => {
  it('is exactly the three actions that end or interrupt what the machine is doing', () => {
    expect([...DISRUPTIVE_POWER_ACTIONS].sort()).toEqual(
      ['ForceOff', 'ForceRestart', 'GracefulShutdown'].sort(),
    )
  })

  it('excludes On, the LED toggle and clearing the log — none of them touch a running workload', () => {
    expect(DISRUPTIVE_POWER_ACTIONS.has('On')).toBe(false)
    expect(DISRUPTIVE_POWER_ACTIONS.has('ClearSEL')).toBe(false)
    expect(DISRUPTIVE_POWER_ACTIONS.has('IndicatorLedOn')).toBe(false)
    expect(DISRUPTIVE_POWER_ACTIONS.has('IndicatorLedOff')).toBe(false)
  })
})

// countPodsOnNode is the one sentence an administrator reads before
// switching off a machine that may be carrying production — see its doc
// comment in machines.ts for why the arithmetic lives here rather than
// inside MachineActions.tsx's fetch callback. This fixture exercises every
// bucket NamespaceNode has (controlled pods, bare pods, an empty namespace)
// and a tree with nothing in it at all, then proves the count actually
// tracks nodeName rather than merely counting pods that happen to match.
describe('countPodsOnNode', () => {
  function pod(name: string, nodeName: string): WorkloadPod {
    return { name, namespace: 'default', phase: 'Running', nodeName, restarts: 0, containers: ['app'] }
  }

  function tree(): NamespaceNode[] {
    return [
      {
        namespace: 'default',
        infrastructure: false,
        controllers: [
          {
            controller: {
              kind: 'Deployment',
              name: 'web',
              namespace: 'default',
              desiredReplicas: 2,
              readyReplicas: 2,
              scalable: true,
            },
            pods: [pod('web-1', 'w2'), pod('web-2', 'other-node')],
          },
        ],
        barePods: [pod('standalone', 'w2')],
        podCount: 3,
      },
      {
        // An empty namespace: no controllers, no bare pods. Must contribute
        // zero without throwing on an empty `controllers`/`barePods` array.
        namespace: 'empty-ns',
        infrastructure: false,
        controllers: [],
        barePods: [],
        podCount: 0,
      },
    ]
  }

  it('counts a pod under a controller and a bare pod on the target node, across namespaces', () => {
    expect(countPodsOnNode(tree(), 'w2')).toBe(2)
  })

  it('does not count a pod scheduled on a different node', () => {
    expect(countPodsOnNode(tree(), 'other-node')).toBe(1)
  })

  it('is zero for a node nothing is scheduled on, including through an empty namespace', () => {
    expect(countPodsOnNode(tree(), 'no-such-node')).toBe(0)
  })

  it('is zero for an empty tree', () => {
    expect(countPodsOnNode([], 'w2')).toBe(0)
  })

  it('moves when a pod moves — proves the filter reads nodeName rather than always matching', () => {
    const withMovedPod = tree()
    withMovedPod[0].controllers[0].pods[1] = pod('web-2', 'w2') // was 'other-node'
    expect(countPodsOnNode(withMovedPod, 'w2')).toBe(3)
    expect(countPodsOnNode(withMovedPod, 'other-node')).toBe(0)
  })
})
