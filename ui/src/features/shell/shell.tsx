import type { ReactNode } from "react"
import { useEffect } from "react"
import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronLeft, PanelLeft, Settings } from "lucide-react"
import { Announcement, Hud } from "@/components/kit/feedback"
import { useDiscovering } from "@/features/machines/queries"
import { StatusBar, StatusProvider } from "@/features/status/status-bar"
import { AppWindow, Toolbar, ToolbarButton } from "@/components/kit/window"
import { buttonVariants } from "@/components/ui/button"
import { useIsPhone } from "./use-is-phone"
import { useSidebar } from "./sidebar"
import { listDetailViews, tabs, useRoutePosition, views } from "./views"
import { ToolbarProvider, ToolbarTarget } from "./toolbar-slot"

export function AppShell({ children }: { children: ReactNode }) {
  useShellShortcuts()
  return (
    <StatusProvider>
      <ToolbarProvider>
        <AppWindow>
          <AppToolbar />
          {children}
          <TabBar />
          <StatusBar />
          <Hud />
        </AppWindow>
      </ToolbarProvider>
    </StatusProvider>
  )
}

// ⌘\ and ⌘1–4 fire even from inputs and the terminal, so they are the only global keys.
function useShellShortcuts() {
  const navigate = useNavigate()
  const { toggle } = useSidebar()
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!event.metaKey || event.altKey || event.shiftKey) return
      if (event.key === "\\") {
        event.preventDefault()
        toggle()
        return
      }
      const view = views[Number(event.key) - 1]
      if (view) {
        event.preventDefault()
        void navigate({ to: view.to })
      }
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [navigate, toggle])
}

function AppToolbar() {
  const { view, detail } = useRoutePosition()
  const isPhone = useIsPhone()
  const { collapsed, toggle } = useSidebar()
  const discovering = useDiscovering()
  const title = views.find((item) => item.id === view)?.label ?? "agentpack"
  const hasSidebar = listDetailViews.includes(view)

  return (
    <Toolbar>
      {hasSidebar && !isPhone && (
        <ToolbarButton
          aria-label="Toggle sidebar"
          aria-expanded={!collapsed}
          title="Toggle sidebar (⌘\)"
          onClick={toggle}
        >
          <PanelLeft />
        </ToolbarButton>
      )}
      {isPhone && detail && (
        <Link
          to={tabs.find((item) => item.id === view)?.to ?? "/sessions"}
          aria-label="Back"
          className={buttonVariants({ variant: "ghost", size: "icon", className: "shrink-0" })}
        >
          <ChevronLeft />
        </Link>
      )}
      <nav aria-label="Views" className="flex shrink-0 items-center gap-(--s1) max-[760px]:hidden">
        {views.map((item, index) => (
          <Link
            key={item.id}
            to={item.to}
            title={`⌘${index + 1}`}
            className={buttonVariants({ variant: item.id === view ? "default" : "ghost", size: "sm" })}
          >
            {item.label}
          </Link>
        ))}
      </nav>
      <span className="min-w-0 truncate text-(--fs) font-medium">{title}</span>
      <div className="flex-1" />
      {discovering && <Announcement>Discovering machines…</Announcement>}
      <ToolbarTarget />
      <Link
        to="/settings"
        aria-label="Settings"
        className={buttonVariants({ variant: "ghost", size: "icon", className: "shrink-0" })}
      >
        <Settings />
      </Link>
    </Toolbar>
  )
}

function TabBar() {
  const { detail } = useRoutePosition()
  if (detail) return null
  return (
    <nav
      aria-label="Tabs"
      className="hidden h-(--tab-h) grid-cols-5 border-t-[0.5px] border-(--sep) bg-(--chrome) max-[760px]:grid"
    >
      {tabs.map((tab) => (
        <Link
          key={tab.id}
          to={tab.to}
          className="flex min-w-0 flex-col items-center justify-center gap-0.5 text-(--fg2) aria-[current=page]:text-(--fg)"
        >
          <tab.icon className="size-4" />
          <span className="text-[10px] leading-none">{tab.label}</span>
        </Link>
      ))}
    </nav>
  )
}
