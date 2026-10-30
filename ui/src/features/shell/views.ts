import { useRouterState } from "@tanstack/react-router"
import { Gauge, Library, Server, Settings, Terminal } from "lucide-react"

export const views = [
  { id: "sessions", label: "Sessions", to: "/sessions", icon: Terminal },
  { id: "library", label: "Library", to: "/library", icon: Library },
  { id: "machines", label: "Machines", to: "/machines", icon: Server },
  { id: "limits", label: "Limits", to: "/limits", icon: Gauge },
] as const

export const tabs = [...views, { id: "settings", label: "Settings", to: "/settings", icon: Settings }] as const

// Views whose index is a list with a detail pane, and so get the sidebar toggle.
export const listDetailViews = ["sessions", "library", "machines"]

// Route position from the URL: the first segment is the view; any further segment is a detail.
export function useRoutePosition() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const [, view = "", ...rest] = pathname.split("/")
  return { view, detail: rest.some(Boolean) }
}
