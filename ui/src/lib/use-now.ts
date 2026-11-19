import { useSyncExternalStore } from "react"

// One shared one-second clock for every ticking component, running only while one is mounted.
const listeners = new Set<() => void>()
let timer: ReturnType<typeof setInterval> | undefined
let now = second()

function second() {
  return Math.floor(Date.now() / 1000) * 1000
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  if (!timer) {
    timer = setInterval(() => {
      now = second()
      for (const notify of listeners) notify()
    }, 1000)
  }
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) {
      clearInterval(timer)
      timer = undefined
    }
  }
}

const idle = () => () => {}

// Whole seconds keep consecutive reads equal, as useSyncExternalStore requires.
function snapshot() {
  if (!timer) now = second()
  return now
}

// The current time, ticking every second while active. Inactive callers get a fresh read per render.
export function useNow(active: boolean): number {
  const ticking = useSyncExternalStore(active ? subscribe : idle, snapshot)
  return active ? ticking : Date.now()
}
