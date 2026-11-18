import { describe, it, expect } from 'vitest'
import { stateAfterFailure, stateAfterSuccess } from './useLiveResource'

describe('stateAfterFailure', () => {
  it('is a plain error before anything was read, opted in or not', () => {
    expect(stateAfterFailure<number[]>({ phase: 'loading' }, 'boom', true)).toEqual({ phase: 'error', message: 'boom' })
  })

  it('keeps the last good data and its read time, marked stale, when the view opts in', () => {
    const after = stateAfterFailure(stateAfterSuccess([1, 2], 1000), '503', true)
    expect(after).toEqual({ phase: 'ready', data: [1, 2], lastRead: 1000, staleError: '503' })
  })

  it('is an error when the view does not opt in', () => {
    expect(stateAfterFailure(stateAfterSuccess([1], 1), '503', false)).toEqual({ phase: 'error', message: '503' })
  })
})

describe('stateAfterFailure status', () => {
  it('carries the HTTP status into the error state, and omits it when there is none', () => {
    expect(stateAfterFailure({ phase: 'loading' }, 'denied', false, 403)).toEqual({
      phase: 'error',
      message: 'denied',
      status: 403,
    })
    expect(stateAfterFailure({ phase: 'loading' }, 'down', false)).not.toHaveProperty('status')
  })
})
