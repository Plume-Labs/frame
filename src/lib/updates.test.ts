import { describe, expect, it } from 'vitest'
import { countUnready, disturbances, parseImage, summarizeVersions } from './updates'
import type { RunningComponent } from './frame-sdk'

const comp = (name: string, image: string, ready = 1, desired = 1): RunningComponent => ({
  name, kind: 'Deployment', image, readyReplicas: ready, desiredReplicas: desired,
})

describe('parseImage', () => {
  it('keeps a registry port out of the tag', () => {
    expect(parseImage('192.168.2.201:30500/frame-ui:c59cbeb')).toEqual({
      repository: '192.168.2.201:30500/frame-ui', version: 'c59cbeb',
    })
    expect(parseImage('registry:5000/frame-ui')).toEqual({ repository: 'registry:5000/frame-ui', version: '' })
  })
  it('shortens a digest', () => {
    expect(parseImage('ghcr.io/x/y@sha256:0123456789abcdef0123').version).toBe('0123456789ab')
  })
})

describe('summarizeVersions', () => {
  it('flags a mixed estate and puts the majority first', () => {
    const s = summarizeVersions([comp('a', 'r/a:1'), comp('b', 'r/b:1'), comp('c', 'r/c:2')])
    expect(s.versions).toEqual(['1', '2'])
    expect(s.mixed).toBe(true)
    expect(s.dominant).toBe('1')
  })
  it('is not mixed when everything agrees, nor when empty', () => {
    expect(summarizeVersions([comp('a', 'r/a:1'), comp('b', 'r/b:1')]).mixed).toBe(false)
    expect(summarizeVersions([])).toEqual({ versions: [], mixed: false, dominant: '' })
  })
})

describe('disturbances', () => {
  it('lists only what is in flight, most serious first', () => {
    const d = disturbances({ runningJobs: 2, installsInProgress: 0, osdReplacementsActive: 1, unreadyComponents: 0 })
    expect(d.map((x) => x.key)).toEqual(['osdReplacementsActive', 'runningJobs'])
  })
  it('is empty on a quiet cluster', () => {
    expect(disturbances({ runningJobs: 0, installsInProgress: 0, osdReplacementsActive: 0, unreadyComponents: 0 })).toEqual([])
  })
})

describe('countUnready', () => {
  it('counts components below their desired replicas', () => {
    expect(countUnready([comp('a', 'r/a:1'), comp('b', 'r/b:1', 0, 2)])).toBe(1)
  })
})
