import { createBrowserHistory, useRouterState, type RouterHistory } from "@tanstack/react-router"
import { ChartColumn, Library, Server, Settings, Terminal } from "lucide-react"
import { onSignOut } from "@/lib/persist"

export const views = [
  { id: "sessions", label: "Sessions", to: "/sessions", icon: Terminal },
  { id: "library", label: "Library", to: "/library", icon: Library },
  { id: "machines", label: "Machines", to: "/machines/topology", icon: Server },
  { id: "usage", label: "Usage", to: "/usage", icon: ChartColumn },
] as const

export const tabs = [...views, { id: "settings", label: "Settings", to: "/settings", icon: Settings }] as const

// Views with a source list beside a detail pane, and so get the sidebar toggle. Settings counts,
// so the view switcher stays put when entering it.
export const listDetailViews = ["sessions", "library", "machines", "usage", "settings"]

export type LastSession = { machine: string; sessionId: string; search: { q?: string; group?: string } }

const LAST_SESSION_KEY = "agentpack-last-session"
onSignOut(() => rememberSession(undefined))

// Per tab, as the terminal pool is: another browser tab keeps its own selection. A new tab
// starts from the chat this browser had open last.
export function lastSession(): LastSession | undefined {
  for (const storage of [() => sessionStorage, () => localStorage]) {
    try {
      const value = JSON.parse(storage().getItem(LAST_SESSION_KEY) ?? "null") as LastSession | null
      if (value?.machine && value.sessionId) return value
    } catch {
      // Blocked storage: try the next one.
    }
  }
  return undefined
}

export function rememberSession(session: LastSession | undefined) {
  for (const storage of [() => sessionStorage, () => localStorage]) {
    try {
      if (session) storage().setItem(LAST_SESSION_KEY, JSON.stringify(session))
      else storage().removeItem(LAST_SESSION_KEY)
    } catch {
      // Private windows may refuse storage; the Sessions view then opens with nothing selected.
    }
  }
}

// Route position from the URL: the first segment is the view; any further segment is a detail.
export function routePosition(pathname: string) {
  const [, view = "", ...rest] = pathname.split("/")
  return { view, detail: rest.some(Boolean), landing: pathname === "/machines/topology" }
}

export function useRoutePosition() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  return routePosition(pathname)
}

declare module "@tanstack/history" {
  interface HistoryState {
    sectionOrigin?: string
  }
}

// Store the section landing URL on each detail entry. Search changes and replacements inside
// that detail keep the same origin, and browser Back and Forward restore it with the entry.
export function createViewHistory() {
  const history = createBrowserHistory()
  const withSectionOrigin = (write: RouterHistory["push"]): RouterHistory["push"] => (href, state, options) => {
    const current = history.location
    const from = routePosition(current.pathname)
    const to = routePosition(href.split(/[?#]/, 1)[0] ?? "")
    const sectionOrigin = to.detail && !to.landing && from.view === to.view
      ? !from.detail || from.landing ? current.href : current.state.sectionOrigin
      : undefined
    write(href, { ...state, sectionOrigin }, options)
  }
  history.push = withSectionOrigin(history.push)
  history.replace = withSectionOrigin(history.replace)
  return history
}
