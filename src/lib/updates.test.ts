import { describe, expect, it } from 'vitest'
import { buildPreparation, countUnready, disturbances, isCommitId, isVersionRef, parseImage, summarizeVersions } from './updates'
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

describe('buildPreparation', () => {
  const base = {
    components: [comp('frame-ui', 'r/frame-ui:abc1234')],
    reasons: [],
    template: 'git checkout {sha}\n# was {current}',
    gitopsUrl: 'https://example.test/gitops',
    now: new Date('2026-11-22T10:00:00Z'),
  }
  it('fills the placeholders and says nothing was changed', () => {
    const out = buildPreparation({ ...base, target: 'def5678' })
    expect(out).toContain('git checkout def5678')
    expect(out).toContain('# was abc1234')
    expect(out).toContain('Nothing has been changed')
    expect(out).toContain('Check again before the sync')
  })
  it('lists what was in flight', () => {
    const out = buildPreparation({ ...base, target: 'def5678', reasons: [{ key: 'runningJobs', count: 2 }] })
    expect(out).toContain('runningJobs=2')
  })
  it('refuses a target that is not a commit id, so nothing odd reaches a shell', () => {
    expect(isCommitId('def5678; rm -rf /')).toBe(false)
    expect(isCommitId('DEF5678')).toBe(false)
    expect(() => buildPreparation({ ...base, target: '$(id)' })).toThrow()
  })
})

describe('another application', () => {
  const base = { components: [], reasons: [], template: 'bump {sha}', gitopsUrl: '', now: new Date('2026-11-22T00:00:00Z') }
  it('accepts a version tag and names the application', () => {
    const out = buildPreparation({ ...base, name: 'Fusora', allowVersionRef: true, target: 'v1.4.2' })
    expect(out).toContain('# Fusora update to v1.4.2')
    expect(out).toContain('bump v1.4.2')
  })
  it('still refuses anything a shell could act on', () => {
    expect(isVersionRef('v1.4.2; rm -rf /')).toBe(false)
    expect(isVersionRef('$(id)')).toBe(false)
    expect(() => buildPreparation({ ...base, allowVersionRef: true, target: 'a b' })).toThrow()
  })
  it('keeps Hearthrack to commit ids', () => {
    expect(() => buildPreparation({ ...base, target: 'v1.4.2' })).toThrow()
  })
})
