import { createContext, useContext } from 'react'

/**
 * Which screen is showing, and how to change it.
 *
 * The screen was a `useState` private to App, so nothing rendered below it
 * could move the user anywhere — every panel was a dead end, and a screen that
 * says "3 nodes are not ready" could not take you to the nodes. Overview needs
 * exactly that, so the state lives here instead.
 *
 * Deliberately not react-router: there is one level of navigation and no URL
 * to keep in sync, so a router would be machinery for a feature we do not have.
 */
export interface Navigation {
  screen: string
  /** `tab` selects a tab within the target screen, when it has them. */
  navigate: (screen: string, tab?: string) => void
  /** Set by `navigate`, read by the target screen to pick its opening tab. */
  pendingTab?: string
}

export const NavigationContext = createContext<Navigation | null>(null)

export function useNavigation(): Navigation {
  const ctx = useContext(NavigationContext)
  if (!ctx) throw new Error('useNavigation must be used inside the App navigation provider')
  return ctx
}
