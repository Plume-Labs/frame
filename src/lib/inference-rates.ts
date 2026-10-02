/**
 * llama.cpp speeds, derived from its monotonic counters.
 *
 * The server's own `llamacpp:prompt_tokens_seconds` / `predicted_tokens_seconds`
 * gauges are averages over the window between two /metrics scrapes, reset on
 * every scrape and fed only when a request finishes. With two readers (this
 * console and Prometheus) and generations longer than the scrape interval they
 * read 0. The counters never reset: tokens served per second of busy time over
 * a window is the real speed.
 *
 * `sum()` on both sides: one value across pods, and the quotient of two empty
 * or zero rates is NaN or absent, never a fabricated 0.
 */
export const DECODE_RATE_QUERY =
  'sum(rate(llamacpp:tokens_predicted_total[5m])) / sum(rate(llamacpp:tokens_predicted_seconds_total[5m]))'

export const PROMPT_RATE_QUERY =
  'sum(rate(llamacpp:prompt_tokens_total[5m])) / sum(rate(llamacpp:prompt_seconds_total[5m]))'

/** First finite sample of a Prometheus vector, or null (empty, NaN, +Inf). */
export function rateFromSamples(samples: Array<{ value: number }> | undefined): number | null {
  const v = samples?.[0]?.value
  return v === undefined || !Number.isFinite(v) ? null : v
}

/** One decimal, or an em dash when there is no value. */
export function formatRate(v: number | null): string {
  return v === null ? '—' : v.toFixed(1)
}
