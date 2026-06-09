import * as React from "react"

const MOBILE_BREAKPOINT = 768

const QUERY = `(max-width: ${MOBILE_BREAKPOINT - 1}px)`

function subscribe(callback: () => void) {
  const mql = window.matchMedia(QUERY)
  mql.addEventListener("change", callback)
  return () => mql.removeEventListener("change", callback)
}

function getSnapshot() {
  return window.innerWidth < MOBILE_BREAKPOINT
}

export function useIsMobile() {
  // useSyncExternalStore subscribes to the media query as an external
  // store — no setState-in-effect, and the value is correct on first
  // render instead of starting as undefined.
  return React.useSyncExternalStore(subscribe, getSnapshot)
}
