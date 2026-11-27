import { useEffect, useState, useSyncExternalStore } from "react"

// Outcomes of actions that finished out of sight. There are no toasts: the toolbar's activity
// viewer shows the latest one in place and keeps a short log, the way Xcode's activity viewer does.
export type Activity = { id: number; tone: "done" | "failed"; message: string; at: number; retry?: () => void; undo?: () => void }

const HISTORY_LIMIT = 20
let log: Activity[] = []
let next = 0
const watchers = new Set<() => void>()
const publish = (list: Activity[]) => {
  log = list
  for (const notify of watchers) notify()
}

export function useActivity(): Activity[] {
  return useSyncExternalStore(
    (notify) => {
      watchers.add(notify)
      return () => watchers.delete(notify)
    },
    () => log,
  )
}

export function dismissActivity(id: number) {
  publish(log.filter((item) => item.id !== id))
}

export function clearActivity() {
  publish([])
}

// Plain functions so any module can report an outcome without a hook.
// Failures stay in the toolbar until dismissed or retried, per the alert model.
export const hud = {
  // `undo` reverses the outcome; the viewer offers it while the outcome shows and in the log.
  success(message: string, undo?: () => void) {
    const failures = log.filter((item) => item.tone === "failed")
    const outcomes = log.filter((item) => item.tone === "done").slice(0, HISTORY_LIMIT - 1)
    publish([...failures, { id: ++next, tone: "done", message, at: Date.now(), undo }, ...outcomes])
  },
  error(message: string, retry?: () => void) {
    publish([{ id: ++next, tone: "failed", message, at: Date.now(), retry }, ...log.filter((item) => item.tone !== "failed" || item.message !== message)])
  },
}

// Copying is silent, as on the desktop: the control that copied says so for a moment (useCopied), and
// only a failure is reported. navigator.clipboard is undefined on plain-HTTP gateway origins.
export function copyText(text: string, failure = "Couldn't copy. Select the text and copy it manually."): Promise<boolean> {
  return (navigator.clipboard?.writeText(text) ?? Promise.reject()).then(
    () => true,
    () => {
      hud.error(failure)
      return false
    },
  )
}

// `copied` is true for 1.5 s after a successful copy, so a button can show a check in place of its icon.
export function useCopied(): [boolean, (text: string, failure?: string) => void] {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1500)
    return () => clearTimeout(timer)
  }, [copied])
  return [copied, (text, failure) => void copyText(text, failure).then(setCopied)]
}
