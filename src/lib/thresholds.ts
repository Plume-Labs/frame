/**
 * Shared threshold → colour-class helpers.
 *
 * Every tuning dashboard used to define its own private copy of this ladder
 * (hitColor, acceptColor, utilColor, offsetColor, capColor, bubbleColor …),
 * half of them hardcoding the literal `text-[oklch(0.75_0.18_75)]` instead of
 * the `--warning` token it duplicates. One ladder, one vocabulary.
 */

/** Semantic tones, mapped to the theme tokens in `src/index.css`. */
export type Tone =
  | 'primary'
  | 'accent'
  | 'foreground'
  | 'muted'
  | 'warning'
  | 'destructive'

export const TONE_TEXT: Record<Tone, string> = {
  primary: 'text-primary',
  accent: 'text-accent',
  foreground: 'text-foreground',
  muted: 'text-muted-foreground',
  warning: 'text-warning',
  destructive: 'text-destructive',
}

/**
 * Higher is better (hit rates, utilisation, acceptance, merge ratios).
 * `good`/`ok` are the lower bounds of the accent/warning bands.
 */
export function scoreTone(value: number, good: number, ok: number): Tone {
  if (value >= good) return 'accent'
  if (value >= ok) return 'warning'
  return 'destructive'
}

/**
 * Lower is better (clock offset, bubble time, latency, packet loss).
 * `good`/`ok` are the upper bounds of the accent/warning bands.
 */
export function inverseScoreTone(value: number, good: number, ok: number): Tone {
  if (value <= good) return 'accent'
  if (value <= ok) return 'warning'
  return 'destructive'
}

/** Convenience: the text class directly, for the common inline case. */
export function scoreClass(value: number, good: number, ok: number): string {
  return TONE_TEXT[scoreTone(value, good, ok)]
}

export function inverseScoreClass(value: number, good: number, ok: number): string {
  return TONE_TEXT[inverseScoreTone(value, good, ok)]
}

/** `1712345678901` → `3h 12m`. Was copy-pasted in MPSDashboard and ResiliencePanel. */
export function formatAge(since: number, now: number = Date.now()): string {
  const seconds = Math.max(0, Math.floor((now - since) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ${minutes % 60}m`
  return `${Math.floor(hours / 24)}d ${hours % 24}h`
}
