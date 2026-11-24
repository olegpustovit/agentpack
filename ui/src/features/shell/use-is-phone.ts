import { useSyncExternalStore } from "react"

// Matches the `max-[760px]` Tailwind variants used by the shell.
export const phoneQuery = "(max-width: 760px)"

function subscribe(onChange: () => void) {
  const media = window.matchMedia(phoneQuery)
  media.addEventListener("change", onChange)
  return () => media.removeEventListener("change", onChange)
}

export function useIsPhone() {
  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia(phoneQuery).matches,
    () => false,
  )
}
