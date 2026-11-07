import { describe, expect, it } from 'vitest'

import { changed, changeWarnings, parametersPatch, settingsProblem, toCache, type Cache } from './caches'

const queue: Cache = toCache({
  metadata: { name: 'jobs', namespace: 'shop' },
  spec: { type: 'cache', parameters: { profile: 'queue', maxMemory: '512Mi', volumeSize: '2Gi', storageClass: 'fast' } },
  status: {
    conditions: [{ type: 'Ready', status: 'True', reason: 'Running' }],
    binding: { endpoint: 'jobs.shop.svc:6379' },
  },
})
const lru: Cache = { ...queue, profile: 'cache', volumeSize: '', storageClass: '' }

describe('toCache', () => {
  it('reads the parameters and the Ready condition', () => {
    expect(queue).toMatchObject({ profile: 'queue', maxMemory: '512Mi', ready: true, reason: 'Running', endpoint: 'jobs.shop.svc:6379' })
  })

  it('says a service nobody reconciled yet is not ready', () => {
    expect(toCache({ metadata: { name: 'a', namespace: 'b' } })).toMatchObject({ ready: false, reason: 'NotReconciled' })
  })
})

describe('settingsProblem', () => {
  it('passes valid settings', () => {
    expect(settingsProblem(queue, { maxMemory: '1Gi', volumeSize: '3Gi' })).toBeNull()
    expect(settingsProblem(lru, { maxMemory: '64Mi', volumeSize: '' })).toBeNull()
  })

  it('refuses a queue volume under three times the dataset, as the provider does', () => {
    expect(settingsProblem(queue, { maxMemory: '1Gi', volumeSize: '2Gi' })).toMatch(/three times/)
  })

  it('refuses a malformed size', () => {
    expect(settingsProblem(queue, { maxMemory: '1G', volumeSize: '3Gi' })).toMatch(/Max memory/)
    expect(settingsProblem(queue, { maxMemory: '1Gi', volumeSize: 'lots' })).toMatch(/Volume size/)
  })
})

describe('changeWarnings', () => {
  it('warns that a cache comes back empty and a queue replays', () => {
    expect(changeWarnings(lru, { maxMemory: '1Gi', volumeSize: '' })[0]).toMatch(/empty/)
    expect(changeWarnings(queue, { maxMemory: '1Gi', volumeSize: '2Gi' })[0]).toMatch(/AOF/)
  })

  it('warns that lowering a queue under its data keeps it down', () => {
    expect(changeWarnings(queue, { maxMemory: '256Mi', volumeSize: '2Gi' }).join(' ')).toMatch(/stays down/)
    expect(changeWarnings(lru, { maxMemory: '256Mi', volumeSize: '' }).join(' ')).not.toMatch(/stays down/)
  })

  it('tells growth from a shrink the provider refuses', () => {
    expect(changeWarnings(queue, { maxMemory: '512Mi', volumeSize: '4Gi' })[0]).toMatch(/grows/)
    expect(changeWarnings(queue, { maxMemory: '512Mi', volumeSize: '1Gi' })[0]).toMatch(/cannot shrink/)
  })
})

describe('parametersPatch', () => {
  it('never sends volumeSize for a cache, which the webhook would refuse', () => {
    expect(parametersPatch(lru, { maxMemory: '1Gi', volumeSize: '' })).toEqual({ spec: { parameters: { maxMemory: '1Gi' } } })
    expect(parametersPatch(queue, { maxMemory: '1Gi', volumeSize: '4Gi' }).spec.parameters.volumeSize).toBe('4Gi')
  })
})

describe('changed', () => {
  it('follows both settings', () => {
    expect(changed(queue, { maxMemory: '512Mi', volumeSize: '2Gi' })).toBe(false)
    expect(changed(queue, { maxMemory: '512Mi', volumeSize: '4Gi' })).toBe(true)
  })
})
