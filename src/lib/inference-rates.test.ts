import { describe, it, expect } from 'vitest'
import { DECODE_RATE_QUERY, PROMPT_RATE_QUERY, formatRate } from './inference-rates'

describe('inference rates', () => {
  it('divides the token rate by the busy-seconds rate, per counter pair', () => {
    expect(DECODE_RATE_QUERY).toContain('llamacpp:tokens_predicted_total')
    expect(DECODE_RATE_QUERY).toContain('llamacpp:tokens_predicted_seconds_total')
    expect(PROMPT_RATE_QUERY).toContain('llamacpp:prompt_tokens_total')
    expect(PROMPT_RATE_QUERY).toContain('llamacpp:prompt_seconds_total')
  })

  it('shows a dash for an unknown rate, never 0 or NaN', () => {
    expect(formatRate(null)).toBe('—')
    expect(formatRate(19.94)).toBe('19.9')
    expect(formatRate(0)).toBe('0.0')
  })
})
