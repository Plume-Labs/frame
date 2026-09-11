import { describe, expect, it } from 'vitest'
import {
  canCreateInstall, confirmationMatches, elapsedLabel, isStalledInPhase,
  isTerminal, phaseIndex, phaseTone, mapInstallPhase, toInstall,
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

describe('who may create one', () => {
  // Creating a FrameInstall wipes disks. The editor tier does not get it, and
  // the screen must agree with the RBAC rather than offer a button the
  // apiserver will refuse.
  it('is admin and nobody else', () => {
    expect(canCreateInstall('admin')).toBe(true)
    expect(canCreateInstall('editor')).toBe(false)
    expect(canCreateInstall('viewer')).toBe(false)
    expect(canCreateInstall('')).toBe(false)
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
