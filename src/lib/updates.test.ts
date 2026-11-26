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
  it('keeps an image with a line break on one comment line', () => {
    const out = buildPreparation({
      ...base,
      target: 'def5678',
      components: [{ name: 'a', kind: 'Deployment', image: 'x\nrm -rf ~ #', readyReplicas: 1, desiredReplicas: 1 }],
    })
    expect(out.split('\n').filter((l) => l.startsWith('rm'))).toEqual([])
  })
})

import { activeRequest, availableRelease, compareVersions, currentVersion, rollbackTarget } from './updates'
import type { ReleaseView, UpdateRequestView } from './frame-sdk'

const rel = (version: string, over: Partial<ReleaseView> = {}): ReleaseView => ({
  name: `r-${version}`, version, channel: version.includes('-') ? 'beta' : 'stable', images: { ui: `ui@${version}` },
  minFrom: '', rollbackSafeTo: '', notes: '', revoked: false, verified: true, publishedAt: '', ...over,
})
const req = (over: Partial<UpdateRequestView>): UpdateRequestView => ({
  name: 'u', createdAt: '2026-11-26T10:00:00Z', targetVersion: '', rollback: false, force: false, phase: 'Succeeded',
  step: '', message: '', fromVersion: '', requestedBy: '', startedAt: '', finishedAt: '', components: [], ...over,
})

describe('compareVersions', () => {
  it('follows the semver.org ordering', () => {
    const asc = ['1.0.0-alpha', '1.0.0-alpha.1', '1.0.0-alpha.beta', '1.0.0-beta', '1.0.0-beta.2', '1.0.0-beta.11', '1.0.0-rc.1', '1.0.0', '1.0.1', '1.1.0', '2.0.0']
    for (let i = 0; i < asc.length; i++) for (let j = 0; j < asc.length; j++) {
      expect(Math.sign(compareVersions(asc[i], asc[j]))).toBe(Math.sign(i - j))
    }
  })
  it('says NaN for what is not a version', () => {
    expect(compareVersions('latest', '1.0.0')).toBeNaN()
    expect(compareVersions('v1.0.0', '1.0.0')).toBeNaN()
  })
})

describe('availableRelease', () => {
  const all = [rel('1.0.0'), rel('1.1.0'), rel('1.2.0', { revoked: true }), rel('1.3.0', { verified: false }), rel('1.4.0-beta.1')]
  it('offers the newest verified, not-withdrawn release newer than what runs', () => {
    expect(availableRelease(all, '1.0.0', 'stable')?.version).toBe('1.1.0')
  })
  it('never offers a revoked or unverified release', () => {
    expect(availableRelease([rel('1.2.0', { revoked: true }), rel('1.3.0', { verified: false })], '1.0.0', 'stable')).toBeNull()
  })
  it('keeps pre-releases to the beta channel', () => {
    expect(availableRelease(all, '1.1.0', 'stable')).toBeNull()
    expect(availableRelease(all, '1.1.0', 'beta')?.version).toBe('1.4.0-beta.1')
  })
  it('offers nothing when already on the newest', () => {
    expect(availableRelease(all, '1.1.0', 'stable')).toBeNull()
  })
  it('offers the newest when the running version is unknown', () => {
    expect(availableRelease(all, '', 'stable')?.version).toBe('1.1.0')
  })
})

describe('currentVersion', () => {
  const releases = [rel('1.0.0'), rel('1.1.0')]
  it('is the release whose images the last successful request left in place', () => {
    const r = req({ components: [{ name: 'ui', from: 'ui@1.0.0', to: 'ui@1.1.0', state: 'Ready' }] })
    expect(currentVersion([r], releases)).toBe('1.1.0')
  })
  it('ignores a failed or rolled-back request, and is empty with no history', () => {
    const failed = req({ phase: 'RolledBack', components: [{ name: 'ui', from: 'ui@1.0.0', to: 'ui@1.1.0', state: 'RolledBack' }] })
    expect(currentVersion([failed], releases)).toBe('')
    expect(currentVersion([], releases)).toBe('')
  })
})

describe('activeRequest', () => {
  it('finds a request the updater has not finished, including one it has not picked up yet', () => {
    expect(activeRequest([req({ phase: '' })])?.phase).toBe('')
    expect(activeRequest([req({ phase: 'Applying' }), req({ phase: 'Succeeded' })])?.phase).toBe('Applying')
    expect(activeRequest([req({ phase: 'Succeeded' }), req({ phase: 'Refused' })])).toBeNull()
  })
})

describe('rollbackTarget', () => {
  const releases = [rel('1.0.0'), rel('1.1.0', { rollbackSafeTo: '1.0.0' }), rel('1.2.0')]
  const up = req({ fromVersion: '1.0.0' })
  it('offers the version before the last update when the running release allows it', () => {
    expect(rollbackTarget([up], releases, '1.1.0')).toBe('1.0.0')
  })
  it('offers nothing when the running release does not offer a rollback, or the floor is higher', () => {
    expect(rollbackTarget([up], releases, '1.2.0')).toBeNull()
    expect(rollbackTarget([req({ fromVersion: '0.9.0' })], releases, '1.1.0')).toBeNull()
  })
  it('offers nothing without a known version or a previous one', () => {
    expect(rollbackTarget([up], releases, '')).toBeNull()
    expect(rollbackTarget([], releases, '1.1.0')).toBeNull()
  })
})
