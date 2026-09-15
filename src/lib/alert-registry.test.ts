import { describe, it, expect } from 'vitest'
import { projectAlerts, projectSubscriptions } from './alert-registry'

describe('projectAlerts', () => {
  const raw = {
    metadata: { name: 'fa-ab12' },
    spec: { fingerprint: 'ab12', alertName: 'KubeCPUOvercommit', severity: 'warning', startsAt: '2026-09-15T11:00:00Z' },
    status: {
      state: 'Firing',
      deliveries: [
        { subscription: 'neura', deliveredState: 'Firing' },
        { subscription: 'other', deliveredState: '', lastError: 'HTTP 401', permanentFailure: true },
      ],
    },
  }

  it('marks a delivery delivered only when its state matches the alert state', () => {
    const [a] = projectAlerts([raw])
    expect(a.deliveries).toEqual([
      { subscription: 'neura', delivered: true, lastError: '', permanent: false },
      { subscription: 'other', delivered: false, lastError: 'HTTP 401', permanent: true },
    ])
  })

  it('sorts firing before resolved, then newest first', () => {
    const resolved = { ...raw, metadata: { name: 'fa-old' }, status: { state: 'Resolved' } }
    const newer = { ...raw, metadata: { name: 'fa-new' }, spec: { ...raw.spec, startsAt: '2026-09-15T11:30:00Z' } }
    expect(projectAlerts([resolved, raw, newer]).map((a) => a.name)).toEqual(['fa-new', 'fa-ab12', 'fa-old'])
  })

  it('skips objects the receiver has not given a state yet', () => {
    expect(projectAlerts([{ ...raw, status: {} }])).toEqual([])
  })

  it('hides excluded deliveries from the badges (finding 1: filter widening must not surface a replay)', () => {
    const withExcluded = {
      ...raw,
      status: {
        state: 'Firing',
        deliveries: [
          { subscription: 'neura', deliveredState: 'Firing' },
          { subscription: 'late', deliveredState: 'Resolved', excluded: true },
        ],
      },
    }
    const [a] = projectAlerts([withExcluded])
    expect(a.deliveries).toEqual([{ subscription: 'neura', delivered: true, lastError: '', permanent: false }])
  })
})

describe('projectSubscriptions', () => {
  it('projects the health fields with safe defaults', () => {
    expect(
      projectSubscriptions([
        { metadata: { name: 'neura' }, spec: { url: 'http://n' }, status: { pendingDeliveries: 2, lastError: 'HTTP 503' } },
      ]),
    ).toEqual([{ name: 'neura', url: 'http://n', paused: false, pending: 2, lastSuccessAt: '', lastError: 'HTTP 503' }])
  })
})
