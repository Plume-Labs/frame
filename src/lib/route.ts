/**
 * The console's location, kept in the URL fragment: `#/<screen>` or
 * `#/<screen>/<tab>`.
 *
 * Navigation is component state, not a router (see `useNavigation`), so a
 * reload, a pasted link or the back button used to land on Overview. This
 * module is only the (de)serialisation of that state, kept pure so it is
 * testable under node. A fragment is never sent to a server, and the
 * `/invite` page's `#token=…` cannot be mistaken for a route: a route always
 * starts with `#/`.
 */
export interface Route {
  screen: string
  tab?: string
}

const ROUTE = /^#\/([a-z0-9-]+)(?:\/([a-z0-9-]+))?$/

/**
 * The route in a location hash, or undefined when the hash is not a route or
 * names a screen the console does not have. Unknown input falls back to the
 * caller's default rather than blanking the page.
 */
export function parseRoute(hash: string, isScreen: (id: string) => boolean): Route | undefined {
  const match = ROUTE.exec(hash)
  if (!match || !isScreen(match[1])) return undefined
  return match[2] ? { screen: match[1], tab: match[2] } : { screen: match[1] }
}

/** The hash for a screen and, optionally, one of its tabs. */
export function formatRoute(screen: string, tab?: string): string {
  return tab ? `#/${screen}/${tab}` : `#/${screen}`
}
