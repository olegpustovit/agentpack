import { useSyncExternalStore } from "react"

type Toast = typeof import("sonner").toast

// The toast library loads with the first toast, not with the app. Sonner shows only toasts created after its
// Toaster subscribes, so each toast waits until the lazily mounted Toaster reports that it is listening.
let ready: Promise<Toast> | undefined
let listening: ((toast: Toast) => void) | undefined
const watchers = new Set<() => void>()

function toaster(): Promise<Toast> {
  if (!ready) {
    ready = new Promise((resolve) => (listening = resolve))
    for (const notify of watchers) notify()
  }
  return ready
}

// True once anything asked for a toast; the shell then mounts the Toaster.
export function useHudRequested(): boolean {
  return useSyncExternalStore(
    (notify) => {
      watchers.add(notify)
      return () => watchers.delete(notify)
    },
    () => ready !== undefined,
  )
}

// Called by the mounted Toaster once it is subscribed.
export function hudListening(toast: Toast) {
  listening?.(toast)
}

// Plain functions so any module can report an outcome without a hook.
// Failures stay until dismissed, per the alert model.
export const hud = {
  success(message: string) {
    void toaster().then((toast) => toast.success(message))
  },
  error(message: string, retry?: () => void) {
    void toaster().then((toast) =>
      toast.error(message, {
        duration: Number.POSITIVE_INFINITY,
        action: retry ? { label: "Retry", onClick: retry } : undefined,
      }),
    )
  },
}

export function copyText(text: string) {
  ;(navigator.clipboard?.writeText(text) ?? Promise.reject()).then(
    () => hud.success("Copied"),
    () => hud.error("Couldn't copy. Select the text and copy it manually."),
  )
}
