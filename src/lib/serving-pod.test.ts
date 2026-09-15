import { describe, it, expect } from 'vitest'
import { servingPod } from './frame-sdk'

const pod = (name: string, phase?: string, deleting = false) => ({
  metadata: { name, ...(deleting ? { deletionTimestamp: '2026-09-15T19:00:00Z' } : {}) },
  status: phase ? { phase } : undefined,
})

describe('servingPod', () => {
  it('skips a Completed pod listed before the Running one (TEI, 2026-09-15)', () => {
    expect(servingPod([pod('tei-old', 'Succeeded'), pod('tei-new', 'Running')])?.metadata.name).toBe('tei-new')
  })

  it('skips Failed, Pending and terminating pods', () => {
    const items = [pod('a', 'Failed'), pod('b', 'Pending'), pod('c', 'Running', true), pod('d', 'Running')]
    expect(servingPod(items)?.metadata.name).toBe('d')
  })

  it('returns undefined when no pod is serving, so callers report the integration as absent', () => {
    expect(servingPod([pod('a', 'Succeeded')])).toBeUndefined()
    expect(servingPod(undefined)).toBeUndefined()
  })
})
