import { describe, expect, it } from 'vitest'
import {
  CONFIRM_SERIAL_HINT, CONFIRM_SERIAL_LABEL,
  canCreateInstall, confirmationMatches, elapsedLabel, installDialogMachineTexts,
  installObjectName, isStalledInPhase, isTerminal, machineOptionLabel, phaseIndex, phaseTone,
  randomNameSuffix,
  mapInstallPhase, textsMentioning, toInstall, type MachineChoice,
} from './installs'

describe('phases', () => {
  it('orders every phase the controller can write', () => {
    expect(phaseIndex('Pending')).toBe(0)
    expect(phaseIndex('Ready')).toBeGreaterThan(phaseIndex('Joining'))
  })

  it('treats Failed as terminal but not as progress', () => {
    expect(isTerminal('Failed')).toBe(true)
    expect(isTerminal('Ready')).toBe(true)
    expect(isTerminal('Installing')).toBe(false)
    expect(phaseTone('Failed')).toBe('bad')
  })
})

describe('elapsed time', () => {
  // Lot 1 shipped a freshness marker computed with `new Date()` at render
  // time, under a watch with no poll: it froze exactly when the operator
  // stopped writing, which is exactly when it mattered. `now` is a parameter
  // here so a test can move it and a component must pass a ticking one.
  it('grows as now moves, with the timestamp fixed', () => {
    const since = '2026-09-11T10:00:00Z'
    expect(elapsedLabel(since, new Date('2026-09-11T10:00:30Z'))).toBe('30 s')
    expect(elapsedLabel(since, new Date('2026-09-11T10:04:00Z'))).toBe('4 min')
    expect(elapsedLabel(since, new Date('2026-09-11T11:30:00Z'))).toBe('1 h 30')
  })

  it('says so rather than guessing when there is no timestamp', () => {
    expect(elapsedLabel(undefined, new Date())).toBe('—')
  })

  // Installing legitimately takes twenty minutes; Preparing does not.
  it('calls a phase stalled against that phase, not against one number', () => {
    const since = '2026-09-11T10:00:00Z'
    const at = (m: number) => new Date(Date.parse(since) + m * 60_000)
    expect(isStalledInPhase('Preparing', since, at(9))).toBe(true)
    expect(isStalledInPhase('Installing', since, at(9))).toBe(false)
    expect(isStalledInPhase('Installing', since, at(65))).toBe(true)
  })

  it('never calls a terminal phase stalled', () => {
    expect(isStalledInPhase('Ready', '2020-01-01T00:00:00Z', new Date())).toBe(false)
    expect(isStalledInPhase('Failed', '2020-01-01T00:00:00Z', new Date())).toBe(false)
  })
})

describe('the confirmation', () => {
  // Typing the serial is the guard. A comparison that ignores case or
  // whitespace is a guard that a paste of the wrong serial also passes.
  it('requires the serial exactly', () => {
    expect(confirmationMatches('CZ3xxxxxxx', 'CZ3xxxxxxx')).toBe(true)
    expect(confirmationMatches(' CZ3xxxxxxx ', 'CZ3xxxxxxx')).toBe(true)
    expect(confirmationMatches('cz3xxxxxxx', 'CZ3xxxxxxx')).toBe(false)
    expect(confirmationMatches('', '')).toBe(false)
    expect(confirmationMatches('CZ3', 'CZ3xxxxxxx')).toBe(false)
  })
})

describe('stall detection', () => {
  const at = (base: string, ms: number) => new Date(Date.parse(base) + ms)
  const since = '2026-09-11T10:00:00Z'

  // `Installed` is entered and left in the same instant — install.go reports
  // it and reports `Joining` on the next statement. Its budget was two
  // minutes, which reads as "this step may take two minutes". An object
  // sitting there means nothing is driving it.
  it('flags Installed within a minute, not two', () => {
    expect(isStalledInPhase('Installed', since, at(since, 90_000))).toBe(true)
  })

  // And the floor is real: phaseSince is a cluster timestamp read against a
  // browser clock, so a few seconds of skew must not read as a stall.
  it('does not flag Installed on clock skew', () => {
    expect(isStalledInPhase('Installed', since, at(since, 10_000))).toBe(false)
  })

  // A phase that legitimately takes twenty minutes must not be flagged at
  // the same threshold — the positive control for the budget being per
  // phase at all.
  it('does not flag Installing at Installed\'s threshold', () => {
    expect(isStalledInPhase('Installing', since, at(since, 90_000))).toBe(false)
  })

  it('never flags a terminal phase, however long ago it landed', () => {
    expect(isStalledInPhase('Ready', since, at(since, 86_400_000))).toBe(false)
    expect(isStalledInPhase('Failed', since, at(since, 86_400_000))).toBe(false)
  })
})

describe('mapInstallPhase', () => {
  it('reads a freshly created object (no status yet) as Pending', () => {
    expect(mapInstallPhase(undefined)).toBe('Pending')
  })

  it('passes through every phase the controller can write', () => {
    for (const p of ['Pending', 'Preparing', 'MediaAttached', 'Installing', 'Installed', 'Joining', 'Ready', 'Failed']) {
      expect(mapInstallPhase(p)).toBe(p)
    }
  })

  it('reads an unrecognised value as Pending rather than throwing or guessing worse', () => {
    expect(mapInstallPhase('SomeFuturePhase')).toBe('Pending')
  })
})

describe('toInstall', () => {
  it('reads every field the screen shows', () => {
    const install = toInstall({
      metadata: { name: 'ml350-g9-install', namespace: 'default' },
      spec: {
        machineRef: 'ml350-g9',
        hostname: 'w3',
        network: { address: '192.168.2.213/24' },
        layout: { kind: 'mirror' },
        cluster: { mode: 'init' },
      },
      status: {
        phase: 'Installing',
        phaseSince: '2026-09-11T10:00:00Z',
        nodeName: '',
        kubeconfigSecret: '',
      },
    })
    expect(install).toEqual({
      name: 'ml350-g9-install',
      namespace: 'default',
      machineRef: 'ml350-g9',
      hostname: 'w3',
      address: '192.168.2.213/24',
      layoutKind: 'mirror',
      clusterMode: 'init',
      phase: 'Installing',
      phaseSince: '2026-09-11T10:00:00Z',
      failedPhase: '',
      message: '',
      nodeName: '',
      kubeconfigSecret: '',
    })
  })

  it('handles an object with no status at all, not just an empty one', () => {
    const install = toInstall({
      metadata: { name: 'fresh', namespace: 'default' },
      spec: {
        machineRef: 'ml350-g9',
        hostname: 'w4',
        network: { address: '192.168.2.214/24' },
        layout: { kind: 'single-disk' },
        cluster: { mode: 'init' },
      },
    })
    expect(install.phase).toBe('Pending')
    expect(install.phaseSince).toBeUndefined()
  })
})

describe('the create dialog never shows the serial it asks for', () => {
  const machine: MachineChoice = {
    name: 'ml350-g9',
    model: 'ProLiant ML350 Gen9',
    serialNumber: 'CZ3xxxxxxx',
  }

  // The positive control. Without it, an empty result below cannot be told
  // apart from a search that never looked at anything — which is the exact
  // shape this lot has been fooled by a dozen times.
  it('textsMentioning finds a serial that IS present', () => {
    expect(textsMentioning(['ml350-g9 — CZ3xxxxxxx'], machine.serialNumber)).toHaveLength(1)
    expect(textsMentioning(['a', 'b CZ3xxxxxxx c', 'd'], machine.serialNumber)).toEqual([
      'b CZ3xxxxxxx c',
    ])
  })

  // And the control that says the search looked at the right texts: the
  // option label is really in the list, and it really does name the machine.
  it('the texts under test are the ones the dialog renders', () => {
    const texts = installDialogMachineTexts([machine])
    expect(texts).toContain(machineOptionLabel(machine))
    expect(texts).toContain(CONFIRM_SERIAL_LABEL)
    expect(machineOptionLabel(machine)).toContain('ml350-g9')
    expect(machineOptionLabel(machine)).toContain('ProLiant ML350 Gen9')
  })

  // Design §8 layer 2: the operator reads the serial off the machine or its
  // BMC. A dialog that prints it beside the box asking for it turns the one
  // "which machine is this" control into a copy from a screen that is
  // already pointing at the wrong one.
  it('no string the dialog renders carries the selected machine’s serial', () => {
    const texts = installDialogMachineTexts([machine])
    expect(textsMentioning(texts, machine.serialNumber)).toEqual([])
  })

  it('and not for any machine in the picker, not just the selected one', () => {
    const others: MachineChoice[] = [
      machine,
      { name: 'w1', model: 'ProLiant DL360 Gen9', serialNumber: 'CZ9yyyyyyy' },
    ]
    const texts = installDialogMachineTexts(others)
    for (const m of others) {
      expect(textsMentioning(texts, m.serialNumber)).toEqual([])
    }
  })

  // Where to find it instead, so removing the display is not just removing
  // information.
  it('says where to read the serial from instead', () => {
    expect(CONFIRM_SERIAL_HINT).toMatch(/BMC/)
  })
})

// Creating a FrameInstall wipes disks. The editor tier does not get it, and
// the screen must agree with the RBAC rather than offer a button the
// apiserver will refuse. Two inputs, both reachable from the one call site:
// the previous string-tier signature had 'editor' and 'viewer' cases no
// caller could ever produce.
describe('who may create one', () => {
  it('is admin and nobody else', () => {
    expect(canCreateInstall(true)).toBe(true)
    expect(canCreateInstall(false)).toBe(false)
  })
})

describe('installObjectName', () => {
  // Design §3: a reinstallation is a second object and the first stays
  // readable. Naming by hostname made a retry collide 409 with the very
  // attempt it was retrying — the one case the design explicitly supports
  // was the one the console could not do.
  it('does not collide with a previous install of the same hostname', () => {
    expect(installObjectName('w3', 'a1b2c3')).not.toBe(installObjectName('w3', 'd4e5f6'))
  })

  it('still reads as belonging to its hostname', () => {
    expect(installObjectName('w3', 'a1b2c3')).toBe('w3-a1b2c3')
  })

  it('stays a legal object name and keeps the suffix when the hostname is long', () => {
    const long = 'a'.repeat(80)
    const name = installObjectName(long, 'a1b2c3')
    expect(name.length).toBeLessThanOrEqual(63)
    expect(name.endsWith('a1b2c3')).toBe(true)
    expect(name).toMatch(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/)
    // And two long hostnames differing only past the cut are still distinct
    // objects, which is the whole point of the suffix.
    expect(installObjectName(long, 'a1b2c3')).not.toBe(installObjectName(long, 'ffffff'))
  })

  it('normalizes what a hostname field can hold', () => {
    expect(installObjectName('W3.Rack1', 'a1b2c3')).toBe('w3-rack1-a1b2c3')
    expect(installObjectName('', 'a1b2c3')).toBe('a1b2c3')
  })
})

describe('randomNameSuffix', () => {
  it('is six hex characters and does not repeat itself', () => {
    const seen = new Set<string>()
    for (let i = 0; i < 50; i++) {
      const s = randomNameSuffix()
      expect(s).toMatch(/^[0-9a-f]{6}$/)
      seen.add(s)
    }
    // 50 draws from 16.7M: a duplicate is possible but a generator that
    // returns a constant is what this catches.
    expect(seen.size).toBeGreaterThan(45)
  })
})
