import { useSyncExternalStore } from "react"

// Matches the `max-[760px]` Tailwind variants used by the shell.
const query = "(max-width: 760px)"

function subscribe(onChange: () => void) {
  const media = window.matchMedia(query)
  media.addEventListener("change", onChange)
  return () => media.removeEventListener("change", onChange)
}

export function useIsPhone() {
  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia(query).matches,
    () => false,
  )
}
