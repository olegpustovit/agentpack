import type { ReactNode } from "react"
import { lazy, Suspense, useEffect, useState } from "react"
import { Link, useNavigate, useParams } from "@tanstack/react-router"
import { ChevronLeft, PanelLeft, Settings } from "lucide-react"
import { Announcement } from "@/components/kit/feedback"
import { ActivityViewer } from "./activity"
import { useDiscovering } from "@/features/machines/queries"
import { useListedSessions } from "@/features/sessions/queries"
import { nextNeedsYou } from "@/features/sessions/session-status"
import { ConnectionItem } from "@/features/status/connection"
import { AppWindow, Toolbar, ToolbarButton } from "@/components/kit/window"
import { buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { hud } from "@/lib/hud"
import { queueLastChatTerminal, requestTerminal, terminalShortcut } from "@/features/terminal/shortcuts"
import { useAttention } from "./attention"
import { useIsPhone } from "./use-is-phone"
import { useSidebar } from "./sidebar"
import { listDetailViews, tabs, useRoutePosition, viewLink, views } from "./views"
import { ToolbarProvider, ToolbarTarget, ToolbarTitleTarget } from "./toolbar-slot"

export function AppShell({ children }: { children: ReactNode }) {
  useShellShortcuts()
  useAttention()
  return (
      <ToolbarProvider>
        <AppWindow>
          <AppToolbar />
          {children}
          <TabBar />
          <ShellCommands />
        </AppWindow>
      </ToolbarProvider>
  )
}

// ⌘\ and ⌘1–4 fire even from inputs and the terminal, so they are the only global keys.
function useShellShortcuts() {
  const navigate = useNavigate()
  const { toggle } = useSidebar()
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey) return
      if (!event.metaKey && (event.target as Element | null)?.closest?.(".xterm")) return
      if (event.key === "\\") {
        event.preventDefault()
        toggle()
        return
      }
      const view = views[Number(event.key) - 1]
      if (view) {
        event.preventDefault()
        void navigate(viewLink(view))
      }
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [navigate, toggle])
}

// Loaded on first open, so the palette stays out of the initial bundle.
const CommandPalette = lazy(() => import("./command-palette"))
const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

// ⌘K opens the palette; elsewhere Ctrl+K, or Ctrl+Shift+K in a terminal, where Ctrl+K is the shell's kill-line.
// ? outside a text field lists shortcuts. ⌘⇧J opens the next session that needs you.
function ShellCommands() {
  const navigate = useNavigate()
  const { sessions } = useListedSessions()
  const params = useParams({ strict: false })
  const { view } = useRoutePosition()
  const machine = view === "machines" ? params.machine : undefined
  const openKey = params.machine && params.sessionId ? `${params.machine}:${params.sessionId}` : undefined
  // The palette's starting query while open; null while closed.
  const [palette, setPalette] = useState<string | null>(null)
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as Element | null
      const inTerminal = Boolean(target?.closest?.(".xterm"))
      const key = event.key.toLowerCase()
      if (key === "k" && !event.altKey && (isMac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey && (event.shiftKey || !inTerminal))) {
        event.preventDefault()
        setPalette((current) => (current === null ? "" : null))
        return
      }
      const command = terminalShortcut(event, isMac)
      if (command) {
        if (command === "next") {
          if (!event.repeat) {
            const next = nextNeedsYou(sessions, openKey)
            if (next) void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: next.machine, sessionId: next.id } })
          }
        } else if (!requestTerminal(command, event.repeat)) {
          if (event.repeat) return
          if (machine) {
            void api<SessionInfo>("/api/sessions", { machine, body: { transport: "shell", place: "home" } }).then(
              (shell) => navigate({ to: "/sessions/$machine/$sessionId", params: { machine, sessionId: shell.id } }),
              (error: Error) => hud.error(`Couldn't open the machine's shell. ${error.message}`),
            )
          } else {
            const chat = queueLastChatTerminal(command)
            if (!chat) return
            void navigate({ to: "/sessions/$machine/$sessionId", params: chat })
          }
        }
        event.preventDefault()
        event.stopPropagation()
        return
      }
      if (event.key === "?" && !event.metaKey && !event.ctrlKey && !event.altKey && !inTerminal && !target?.closest?.("input, textarea, select, [contenteditable='true']")) {
        event.preventDefault()
        setPalette("?")
      }
    }
    window.addEventListener("keydown", onKeyDown, true)
    return () => window.removeEventListener("keydown", onKeyDown, true)
  }, [navigate, sessions, openKey, machine])
  if (palette === null) return null
  return (
    <Suspense fallback={null}>
      <CommandPalette initialQuery={palette} sessions={sessions} openKey={openKey} onClose={() => setPalette(null)} />
    </Suspense>
  )
}

function AppToolbar() {
  const { view, detail, landing } = useRoutePosition()
  const isPhone = useIsPhone()
  const { collapsed, toggle } = useSidebar()
  const discovering = useDiscovering()
  const title = tabs.find((item) => item.id === view)?.label ?? "Agentpack"
  const hasSidebar = listDetailViews.includes(view)

  return (
    <Toolbar>
      {hasSidebar && !isPhone && (
        <ToolbarButton
          aria-label="Toggle sidebar"
          aria-expanded={!collapsed}
          title="Toggle sidebar (⌘\)"
          onClick={toggle}
          className="aria-expanded:text-(--accent)"
        >
          <PanelLeft />
        </ToolbarButton>
      )}
      {isPhone && detail && !landing && (
        <Link
          to={view === "machines" ? "/machines" : tabs.find((item) => item.id === view)?.to ?? "/sessions"}
          aria-label="Back"
          className={buttonVariants({ variant: "ghost", size: "icon", className: "shrink-0" })}
        >
          <ChevronLeft />
        </Link>
      )}
      <nav aria-label="Views" className="me-(--s3) flex h-(--ctl-h) shrink-0 items-center rounded-full bg-(--track) p-0.5 max-[760px]:hidden">
        {views.map((item, index) => (
          <Link
            key={item.id}
            {...viewLink(item)}
            title={`⌘${index + 1}`}
            className={cn(
              "flex h-full cursor-default items-center rounded-full px-(--s5) text-(length:--fs-sm) font-medium text-(--fg2)",
              item.id === view && "bg-(--control) text-(--fg) shadow-(--shadow-ctl)",
            )}
          >
            {item.label}
          </Link>
        ))}
      </nav>
      <ToolbarTitleTarget />
      <span className="hidden min-w-24 flex-1 truncate text-(length:--fs) font-semibold peer-empty:inline">{title}</span>
      {discovering && <Announcement>Discovering machines…</Announcement>}
      <ConnectionItem />
      <ActivityViewer />
      <ToolbarTarget />
      <Link
        to="/settings"
        aria-label="Settings"
        className={buttonVariants({ variant: "ghost", size: "icon", className: cn("shrink-0 rounded-full [&_svg]:size-4 max-[760px]:hidden", view === "settings" && "text-(--accent)") })}
      >
        <Settings />
      </Link>
    </Toolbar>
  )
}

function TabBar() {
  const { sessions } = useListedSessions()
  const needsYou = sessions.filter((session) => session.conversation_state === "needs_you").length
  return (
    <nav
      aria-label="Tabs"
      data-slot="tab-bar"
      className="hidden grid-cols-5 border-t-[0.5px] border-(--sep) bg-(--chrome) max-[760px]:grid"
    >
      {tabs.map((tab) => (
        <Link
          key={tab.id}
          to={tab.to}
          aria-label={tab.id === "sessions" && needsYou ? `${tab.label}, ${needsYou} need you` : undefined}
          className="relative flex min-w-0 flex-col items-center justify-center gap-1 pt-1 text-(--fg2) aria-[current=page]:text-(--accent)"
        >
          <tab.icon aria-hidden className="size-6" />
          <span className="text-[11px] leading-none font-medium">{tab.label}</span>
          {tab.id === "sessions" && needsYou > 0 && (
            <span aria-hidden className="absolute top-1 left-[calc(50%+6px)] min-w-[18px] rounded-full bg-(--warn) px-1 text-center text-[11px] leading-[18px] font-semibold text-black">{needsYou}</span>
          )}
        </Link>
      ))}
    </nav>
  )
}
