/**
 * Name for a resubmitted job: the original's name and a short random suffix, so
 * retrying twice never collides with the first retry or the original.
 *
 * Kept out of the component because a random value belongs in an event
 * handler's helper, never in a render body.
 */
export function retryJobName(name: string, random: () => number = Math.random): string {
  return `${name}-retry-${random().toString(36).slice(2, 7)}`
}
