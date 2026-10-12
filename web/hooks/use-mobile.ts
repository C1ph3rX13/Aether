import { useSyncExternalStore } from "react"

// Use the official Sidebar breakpoint independently of Project inspection.
const query = "(width < 768px)"
function subscribe(onChange: () => void) {
  const media = window.matchMedia(query)
  media.addEventListener("change", onChange)
  return () => media.removeEventListener("change", onChange)
}
const getSnapshot = () => window.matchMedia(query).matches
const getServerSnapshot = () => false

export function useIsMobile() {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)
}
