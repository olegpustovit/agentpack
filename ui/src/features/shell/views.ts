import { linkOptions, useRouterState } from "@tanstack/react-router"
import { Gauge, Library, Server, Settings, Terminal } from "lucide-react"

export const views = [
  { id: "sessions", label: "Sessions", to: "/sessions", icon: Terminal },
  { id: "library", label: "Library", to: "/library", icon: Library },
  { id: "machines", label: "Machines", to: "/machines/topology", icon: Server },
  { id: "limits", label: "Limits", to: "/limits", icon: Gauge },
] as const

export const tabs = [...views, { id: "settings", label: "Settings", to: "/settings", icon: Settings }] as const

// Views whose index is a list with a detail pane, and so get the sidebar toggle.
export const listDetailViews = ["sessions", "library", "machines"]

export type LastSession = { machine: string; sessionId: string; search: { q?: string; group?: string } }

const LAST_SESSION_KEY = "agentpack-last-session"

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

// The Sessions view reopens the terminal it left. A view's own link never loses what was selected in it.
export function viewLink(view: (typeof views)[number]) {
  const session = view.id === "sessions" ? lastSession() : undefined
  return session
    ? linkOptions({ to: "/sessions/$machine/$sessionId", params: { machine: session.machine, sessionId: session.sessionId }, search: session.search })
    : linkOptions({ to: view.to })
}

// Route position from the URL: the first segment is the view; any further segment is a detail.
export function useRoutePosition() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const [, view = "", ...rest] = pathname.split("/")
  // The topology is where the Machines tab lands, so on a phone it keeps the tab bar and has no Back.
  return { view, detail: rest.some(Boolean), landing: pathname === "/machines/topology" }
}
