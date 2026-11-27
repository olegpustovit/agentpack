import type { ReactNode } from "react"
import { lazy, Suspense, useEffect, useState } from "react"
import { Link, useNavigate, useParams, useRouter, useRouterState } from "@tanstack/react-router"
import { ChevronLeft, Command, PanelLeft } from "lucide-react"
import { Announcement } from "@/components/kit/feedback"
import { slidingThumb, useSlidingThumb } from "@/components/kit/list"
import { ActivityViewer } from "./activity"
import { useDiscovering } from "@/features/machines/queries"
import { sessionsQuery, useListedSessions } from "@/features/sessions/queries"
import { nextNeedsYou, sessionGlyphState } from "@/features/sessions/session-status"
import { ConnectionItem } from "@/features/status/connection"
import { AppWindow, Toolbar, ToolbarButton } from "@/components/kit/window"
import { buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { hud } from "@/lib/hud"
import { queueLastChatTerminal, requestTerminal, terminalShortcut } from "@/features/terminal/shortcuts"
import { sections } from "@/features/settings/sections"
import { useAttention } from "./attention"
import { useIsPhone } from "./use-is-phone"
import { useSidebar } from "./sidebar"
import { listDetailViews, routePosition, useRoutePosition, views } from "./views"
import { ToolbarProvider, ToolbarTarget, ToolbarTitleTarget } from "./toolbar-slot"

export function AppShell({ children }: { children: ReactNode }) {
  useShellShortcuts()
  useAttention()
  useDetailHeadingFocus()
  useViewEnter()
  // The palette's starting query while open; null while closed. The toolbar's Commands button opens it too.
  const [palette, setPalette] = useState<string | null>(null)
  return (
    <ToolbarProvider>
      <AppWindow>
        {/* Move focus without changing the route's URL. */}
        <a
          href="#main"
          onClick={(event) => {
            event.preventDefault()
            document.getElementById("main")?.focus()
          }}
          className="fixed top-(--s2) left-(--s2) z-50 -translate-y-24 rounded-(--r-ctl) bg-(--control) px-(--s4) py-(--s2) font-medium shadow-(--shadow-ctl) focus:translate-y-0"
        >
          Skip to content
        </a>
        <AppToolbar onCommands={() => setPalette("")} />
        <main id="main" tabIndex={-1} className="grid min-h-0 min-w-0 grid-cols-[minmax(0,1fr)] grid-rows-[minmax(0,1fr)] outline-none">{children}</main>
        <TabBar />
        <ShellCommands palette={palette} setPalette={setPalette} />
      </AppWindow>
    </ToolbarProvider>
  )
}

// Switching views (Sessions, Library, …) fades the new pane in with a hint of lift; within a view nothing moves.
function useViewEnter() {
  const router = useRouter()
  useEffect(() => router.subscribe("onRendered", ({ fromLocation, toLocation }) => {
    if (!fromLocation || routePosition(fromLocation.pathname).view === routePosition(toLocation.pathname).view) return
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) return
    const token = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()
    const duration = token("--dur-2")
    document.getElementById("main")?.animate(
      [{ opacity: 0, translate: `0 ${token("--enter-shift")}` }, { opacity: 1, translate: "0 0" }],
      { duration: parseFloat(duration) * (duration.endsWith("ms") ? 1 : 1000), easing: token("--ease") },
    )
  }), [router])
}

function useDetailHeadingFocus() {
  const router = useRouter()
  useEffect(() => router.subscribe("onRendered", ({ pathChanged, fromLocation, toLocation }) => {
    const { view, detail } = routePosition(toLocation.pathname)
    if (!fromLocation || !pathChanged || !detail) return
    if (view === "sessions") {
      const sessionId = toLocation.pathname.split("/")[3]
      const session = router.options.context.queryClient.getQueryData(sessionsQuery(false).queryKey)?.find((item) => encodeURIComponent(item.id) === sessionId)
      // Terminal entry owns focus. Arrowing through the source list leaves its roving focus in place.
      if (session?.transport !== "acp" || document.activeElement?.closest("[data-row-key]")) return
    }
    requestAnimationFrame(() => {
      if (router.state.location.href !== toLocation.href || document.querySelector("[aria-modal=true]")) return
      const focused = document.activeElement
      if (focused?.closest(".xterm, textarea, input, [contenteditable=true]")) return
      const headings = document.querySelectorAll<HTMLElement>('[data-slot="toolbar"] h1')
      Array.from(headings).find((heading) => heading.getClientRects().length > 0)?.focus({ preventScroll: true })
    })
  }), [router])
}

// ⌘\ and ⌘1–3 fire even from inputs and the terminal, so they are the only global keys.
function useShellShortcuts() {
  const navigate = useNavigate()
  const { toggle } = useSidebar()
  useEffect(() => {
    // G then 1–3, outside text fields: plain keys, which no browser, OS or password manager takes.
    let goAt = -Infinity
    const onKeyDown = (event: KeyboardEvent) => {
      if (!event.metaKey && !event.ctrlKey && !event.altKey) {
        if (event.repeat || (event.target as Element | null)?.closest?.("input, textarea, select, [contenteditable='true'], .xterm")) return
        const view = event.timeStamp - goAt < 1500 ? views[Number(/^(?:Digit|Numpad)([1-9])$/.exec(event.code)?.[1]) - 1] : undefined
        goAt = event.code === "KeyG" && !event.shiftKey ? event.timeStamp : -Infinity
        if (!view) return
        event.preventDefault()
        void navigate({ to: view.to })
        return
      }
      if (!(event.metaKey || event.ctrlKey) || event.shiftKey || event.repeat) return
      // ⌥⌘ as well: Safari keeps ⌘1–9, macOS ⌃1–9 (Desktops) and 1Password ⌘\ for themselves. Ctrl+Alt off Mac is AltGr.
      if (event.altKey && !event.metaKey) return
      if (!event.metaKey && (event.target as Element | null)?.closest?.(".xterm")) return
      // The physical key, so layouts whose digit row types punctuation still switch views.
      const digit = /^(?:Digit|Numpad)([1-9])$/.exec(event.code)?.[1]
      const view = digit ? views[Number(digit) - 1] : undefined
      const sidebar = event.altKey ? event.code === "Backslash" : event.key === "\\"
      if (!sidebar && !view) return
      event.preventDefault()
      event.stopPropagation()
      if (view) void navigate({ to: view.to })
      else toggle()
    }
    window.addEventListener("keydown", onKeyDown, true)
    return () => window.removeEventListener("keydown", onKeyDown, true)
  }, [navigate, toggle])
}

// Loaded on first open, so the palette stays out of the initial bundle.
const CommandPalette = lazy(() => import("./command-palette"))
const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

// ⌘K opens the palette; elsewhere Ctrl+K, or Ctrl+Shift+K in a terminal, where Ctrl+K is the shell's kill-line.
// ? outside a text field lists shortcuts. ⌘⇧J opens the next session that needs you.
function ShellCommands({ palette, setPalette }: { palette: string | null; setPalette: (update: string | null | ((current: string | null) => string | null)) => void }) {
  const navigate = useNavigate()
  const { sessions } = useListedSessions()
  const params = useParams({ strict: false })
  const { view } = useRoutePosition()
  const machine = view === "system" ? params.machine : undefined
  const openKey = params.machine && params.sessionId ? `${params.machine}:${params.sessionId}` : undefined
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
  }, [navigate, sessions, openKey, machine, setPalette])
  return (
    <>
    {palette !== null && <Suspense fallback={null}>
      <CommandPalette initialQuery={palette} sessions={sessions} openKey={openKey} onClose={() => setPalette(null)} />
    </Suspense>}
    </>
  )
}

function AppToolbar({ onCommands }: { onCommands: () => void }) {
  const { view, detail } = useRoutePosition()
  const origin = useRouterState({ select: (state) => state.location.state.sectionOrigin })
  const subagent = useRouterState({ select: (state) => view === "sessions" && Boolean(state.location.search.sub) })
  const isPhone = useIsPhone()
  const { collapsed, toggle } = useSidebar()
  const discovering = useDiscovering()
  const tab = views.find((item) => item.id === view)
  const title = tab?.label ?? "Agentpack"
  const pane = useParams({ strict: false, select: (params) => params.pane })
  const parent = isPhone && pane ? sections.find((section) => section.id === pane)?.parent : undefined
  const backTo = view === "system" ? "/system" : tab?.to ?? "/sessions"
  const hasSidebar = listDetailViews.includes(view)
  const viewsTrack = useSlidingThumb<HTMLElement>('[aria-current="page"]')

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
      {isPhone && detail && !subagent && (
        <Link
          to={parent ? "/settings/$pane" : backTo}
          params={parent ? { pane: parent } : undefined}
          href={parent ? undefined : origin}
          aria-label={`Back to ${parent ? "General" : title}`}
          className={buttonVariants({ variant: "ghost", size: "icon", className: "shrink-0" })}
        >
          <ChevronLeft />
        </Link>
      )}
      <nav ref={viewsTrack} aria-label="Views" className={cn("me-(--s3) flex h-(--ctl-h) shrink-0 items-center rounded-full bg-(--track) p-0.5 max-[760px]:hidden", slidingThumb, "before:inset-y-0.5 before:rounded-full before:bg-(--control) before:shadow-(--shadow-ctl)")}>
        {views.map((item, index) => (
          <Link
            key={item.id}
            to={item.to}
            aria-current={item.id === view ? "page" : undefined}
            title={isMac ? `⌥⌘${index + 1}` : `Ctrl+${index + 1}`}
            className={cn(
              "flex h-full cursor-default items-center rounded-full px-(--s5) text-(length:--fs-sm) font-medium text-(--fg2) transition-colors duration-(--dur-2) ease-(--ease)",
              item.id === view && "text-(--fg)",
            )}
          >
            {item.label}
          </Link>
        ))}
      </nav>
      <ToolbarTitleTarget />
      <h1 tabIndex={-1} className="hidden min-w-24 flex-1 truncate text-(length:--fs) font-semibold peer-empty:inline">{title}</h1>
      {discovering && <Announcement>Discovering machines…</Announcement>}
      <ConnectionItem />
      <ActivityViewer />
      <ToolbarTarget />
      <ToolbarButton aria-label="Command palette" aria-haspopup="dialog" title={isMac ? "Command palette (⌘K)" : "Command palette (Ctrl+K)"} onClick={onCommands} className="max-[760px]:ms-auto max-[760px]:size-11">
        <Command aria-hidden />
      </ToolbarButton>
    </Toolbar>
  )
}

function TabBar() {
  const { sessions } = useListedSessions()
  const { view, detail } = useRoutePosition()
  const needsYou = sessions.filter((session) => sessionGlyphState(session) === "needsYou").length
  if (view === "sessions" && detail) return null
  return (
    <nav
      aria-label="Tabs"
      data-slot="tab-bar"
      className="hidden grid-cols-3 border-t-[0.5px] border-(--sep) bg-(--chrome) max-[760px]:grid"
    >
      {views.map((tab) => (
        <Link
          key={tab.id}
          // System opens on Usage; tapping it again opens its list.
          to={tab.id === "system" && view === "system" ? "/system" : tab.to}
          aria-current={tab.id === view ? "page" : undefined}
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
