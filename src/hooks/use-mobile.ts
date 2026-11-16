import { useSyncExternalStore } from "react"

const MOBILE_BREAKPOINT = 768

function subscribe(onChange: () => void) {
  const mql = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`)
  mql.addEventListener("change", onChange)
  return () => mql.removeEventListener("change", onChange)
}

const isMobileNow = () => window.innerWidth < MOBILE_BREAKPOINT

export function useIsMobile() {
  return useSyncExternalStore(subscribe, isMobileNow, () => false)
}
