import { useEffect, useState } from 'react'

/**
 * The current time as React state, refreshed every `tickMs`.
 *
 * A view that judges how old a reading is needs "now" while it renders, but
 * `Date.now()` in a render body is impure: two renders of the same props can
 * disagree, and React Compiler refuses to memoize such a component. Held in
 * state it is read like any other value, and the interval is what moves it.
 *
 * It is 15 s by default, well under the 120 s after which an agent's reading
 * counts as stale, and `agentFreshness` clamps a reading that is newer than
 * `now` to age 0, so a refresh that lands between two ticks never reads negative.
 */
export function useNow(tickMs = 15_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), tickMs)
    return () => clearInterval(id)
  }, [tickMs])
  return now
}
