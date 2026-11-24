import { useEffect, useRef, useState } from "react"
import { useNavigate } from "@tanstack/react-router"
import { Bell, X } from "lucide-react"
import { providerGroups, tightestWindow, type ProviderGroup } from "@/features/limits/model"
import { limitsQuery } from "@/features/limits/queries"
import { useReplicatedQueries, type Machine } from "@/features/machines/queries"
import { sessionLabel, type ListedSession } from "@/features/sessions/group"
import { useListedSessions } from "@/features/sessions/queries"
import { sessionGlyphState, type GlyphState } from "@/features/sessions/session-status"
import { LOCAL } from "@/lib/api"
import { basename, harnessName } from "@/lib/format"

export type AlertKind = "needs" | "finished" | "limit" | "offline"
export type NotifyPrefs = Record<AlertKind, boolean>
export type Alert = { kind: AlertKind; tag: string; title: string; body: string; session?: { machine: string; id: string } }

// What the transitions compare: each session's state, each provider's tightest window, each peer's reachability.
export type Watched = {
  sessions: Map<string, { state: GlyphState; session: ListedSession }>
  limits: Map<string, number>
  machines: Map<string, Machine>
}

export const LIMIT_WARN = 20

export function watch(sessions: ListedSession[], groups: ProviderGroup[], machines: Machine[]): Watched {
  const limits = new Map<string, number>()
  for (const group of groups) {
    const left = tightestWindow([group])?.left
    if (left !== undefined) limits.set(group.provider, left)
  }
  return {
    sessions: new Map(sessions.map((session) => [`${session.machine}:${session.id}`, { state: sessionGlyphState(session), session }])),
    limits,
    // The gateway serving this page is never "offline" to it.
    machines: new Map(machines.filter((machine) => machine.id !== LOCAL).map((machine) => [machine.id, machine])),
  }
}

export function needsYouCount(watched: Watched): number {
  return [...watched.sessions.values()].filter((entry) => entry.state === "needsYou").length
}

// Only changes since the previous reading alert; the first reading is the baseline, so opening a tab is quiet.
export function attentionAlerts(before: Watched, after: Watched): Alert[] {
  const alerts: Alert[] = []
  // A session's tag matches its Web Push notifications, so the two replace each other instead of stacking.
  for (const [key, { state, session }] of after.sessions) {
    const was = before.sessions.get(key)?.state
    const where = [harnessName(session.harness), basename(session.directory), session.machineName].filter(Boolean).join(" · ")
    const target = { machine: session.machine, id: session.id }
    if (state === "needsYou" && was !== "needsYou") alerts.push({ kind: "needs", tag: `session:${session.id}`, title: `${sessionLabel(session)} needs you`, body: where, session: target })
    // A turn ended with nothing left in the background (waiting comes first while work runs). A
    // terminal reads running only while its hooks report a turn, so it finishes the same way.
    if (state === "idle" && (was === "running" || was === "waiting")) alerts.push({ kind: "finished", tag: `session:${session.id}`, title: `${sessionLabel(session)} finished`, body: where, session: target })
  }
  for (const [provider, left] of after.limits) {
    const was = before.limits.get(provider)
    if (was !== undefined && was > LIMIT_WARN && left <= LIMIT_WARN) {
      alerts.push({ kind: "limit", tag: `limit:${provider}`, title: `${harnessName(provider)} limit is low`, body: `${Math.round(left)}% left` })
    }
  }
  for (const [id, machine] of after.machines) {
    if (!machine.online && before.machines.get(id)?.online) {
      alerts.push({ kind: "offline", tag: `offline:${id}`, title: `${machine.name} went offline`, body: "Its sessions stay listed; live controls wait until it is back." })
    }
  }
  return alerts
}

// Per device on purpose: a phone and a desktop want different noise.
const PREFS_KEY = "agentpack.notify"
export const defaultPrefs: NotifyPrefs = { needs: true, finished: true, limit: true, offline: false }

export function loadNotifyPrefs(): NotifyPrefs {
  try {
    const saved: unknown = JSON.parse(localStorage.getItem(PREFS_KEY) ?? "null")
    const value = saved && typeof saved === "object" ? (saved as Partial<Record<string, unknown>>) : {}
    return Object.fromEntries(Object.entries(defaultPrefs).map(([key, fallback]) => [key, typeof value[key] === "boolean" ? value[key] : fallback])) as NotifyPrefs
  } catch {
    return defaultPrefs
  }
}

export function saveNotifyPrefs(prefs: NotifyPrefs) {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs))
  } catch {
    // Blocked storage: the defaults apply on the next load.
  }
}

export function notificationPermission(): NotificationPermission | "unsupported" {
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission
}

// shortcut: page-level Notification only, so Android Chrome (which requires a service worker) and closed
// tabs get nothing; Web Push (M9) replaces this with showNotification from the service worker.
export function showNotification(title: string, options: NotificationOptions, onClick?: () => void) {
  try {
    const notification = new Notification(title, options)
    notification.onclick = () => {
      window.focus()
      onClick?.()
      notification.close()
    }
  } catch {
    // Browsers that only notify from a service worker.
  }
}

type Badged = Navigator & { setAppBadge?: (count?: number) => Promise<void>; clearAppBadge?: () => Promise<void> }

const TITLE = "Agentpack"

export function attentionTitle(count: number): string {
  return count > 0 ? `(${count}) ${TITLE}` : TITLE
}

// Mounted once in the shell. Reads the sessions, limits and machine queries the views already share,
// keeps the tab title and app badge on the "Needs you" count, and notifies while the tab is hidden.
export function useAttention() {
  const navigate = useNavigate()
  const { machines, queries, sessions } = useListedSessions()
  const limits = useReplicatedQueries(limitsQuery)
  const groups = providerGroups(machines.flatMap((machine) => (limits.byMachine[machine.id]?.data ?? []).map((snapshot) => ({ id: machine.id, name: machine.name, snapshot }))))
  const current = watch(sessions, groups, machines)
  const count = needsYouCount(current)
  const previous = useRef<Watched | undefined>(undefined)
  const loading = queries.firstLoadPending

  useEffect(() => {
    document.title = attentionTitle(count)
    const badged = navigator as Badged
    void (count > 0 ? badged.setAppBadge?.(count) : badged.clearAppBadge?.())?.catch(() => undefined)
  }, [count])
  useEffect(
    () => () => {
      document.title = TITLE
      void (navigator as Badged).clearAppBadge?.()?.catch(() => undefined)
    },
    [],
  )

  useEffect(() => {
    if (loading) return
    const before = previous.current
    previous.current = current
    if (!before || document.visibilityState !== "hidden" || notificationPermission() !== "granted") return
    const prefs = loadNotifyPrefs()
    for (const alert of attentionAlerts(before, current)) {
      if (!prefs[alert.kind]) continue
      const target = alert.session
      showNotification(alert.title, { body: alert.body, tag: alert.tag }, target && (() => void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: target.machine, sessionId: target.id } })))
    }
  })
}

// Asks once; the browser remembers the answer for this site.
export function usePermission() {
  const [permission, setPermission] = useState(notificationPermission)
  const ask = () => void Notification.requestPermission().then(setPermission, () => setPermission(notificationPermission()))
  return { permission, ask }
}

const OFFER_KEY = "agentpack.notify.offered"

function offerDismissed(): boolean {
  try {
    return localStorage.getItem(OFFER_KEY) === "1"
  } catch {
    return true
  }
}

// Offered where it matters: on the Needs you header, only while something needs you and the browser hasn't been asked.
export function NotifyOffer() {
  const { permission, ask } = usePermission()
  const [dismissed, setDismissed] = useState(offerDismissed)
  if (permission !== "default" || dismissed) return null
  function close() {
    try {
      localStorage.setItem(OFFER_KEY, "1")
    } catch {
      // Shown again next load.
    }
    setDismissed(true)
  }
  return (
    <span className="activity-in ms-auto flex shrink-0 items-center gap-(--s1)">
      <button type="button" title="Get a notification when an agent needs you" onClick={() => { ask(); close() }}
        className="flex h-(--ctl-h-sm) items-center gap-(--s2) rounded-full px-(--s3) text-(length:--fs-xs) font-semibold text-(--accent) outline-none hover:bg-(--accent-soft) focus-visible:ring-2 focus-visible:ring-(--accent)">
        <Bell aria-hidden className="size-3.5" />Notify me
      </button>
      <button type="button" aria-label="Don't offer notifications" title="Don't offer notifications" onClick={close}
        className="grid size-(--ctl-h-sm) place-items-center rounded-full text-(--fg3) outline-none hover:bg-(--hover) hover:text-(--fg2) focus-visible:ring-2 focus-visible:ring-(--accent)">
        <X aria-hidden className="size-3" />
      </button>
    </span>
  )
}
