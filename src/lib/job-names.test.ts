import { describe, it, expect } from 'vitest'
import { retryJobName } from './job-names'

describe('retryJobName', () => {
  it('keeps the original name and adds a five-character suffix', () => {
    expect(retryJobName('train-a', () => 0.123456789)).toMatch(/^train-a-retry-[a-z0-9]{5}$/)
  })

  it('differs between two retries of the same job', () => {
    expect(retryJobName('train-a', () => 0.1)).not.toBe(retryJobName('train-a', () => 0.9))
  })
})
