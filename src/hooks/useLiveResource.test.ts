import { describe, it, expect } from 'vitest'
import { stateAfterFailure, stateAfterSuccess, type LiveState } from './useLiveResource'

describe('stateAfterFailure', () => {
  it('is a plain error before anything was read', () => {
    expect(stateAfterFailure<number[]>({ phase: 'loading' }, 'boom')).toEqual({ phase: 'error', message: 'boom' })
  })

  it('keeps the last good data and marks it stale after a failed refresh', () => {
    const before = stateAfterSuccess([1, 2], 1000)
    const after = stateAfterFailure(before, '503')
    expect(after).toMatchObject({ phase: 'ready', data: [1, 2], staleError: '503', lastRead: 1000 })
  })

  it('a later success clears the stale marker', () => {
    const stale: LiveState<number[]> = stateAfterFailure(stateAfterSuccess([1], 1), 'x')
    const fresh = stateAfterSuccess([2], 2)
    expect(fresh).toEqual({ phase: 'ready', data: [2], lastRead: 2 })
    expect((stale as { staleError?: string }).staleError).toBe('x')
  })
})
