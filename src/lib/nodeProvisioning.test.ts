import { describe, expect, it } from 'vitest'
import { parseDnsList } from './nodeProvisioning'

describe('parseDnsList', () => {
  it('parses comma-separated values and trims whitespace', () => {
    expect(parseDnsList('1.1.1.1, 8.8.8.8 ,9.9.9.9')).toEqual(['1.1.1.1', '8.8.8.8', '9.9.9.9'])
  })

  it('removes empty entries', () => {
    expect(parseDnsList('1.1.1.1, , ,8.8.8.8')).toEqual(['1.1.1.1', '8.8.8.8'])
  })
})
