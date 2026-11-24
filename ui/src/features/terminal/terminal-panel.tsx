import "@xterm/xterm/css/xterm.css"
import { useEffect, useRef, useState, useSyncExternalStore } from "react"
import { useMutation, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { ChevronDown, Maximize2, Minimize2, Plus, SquareTerminal, X } from "lucide-react"
import { Banner, Spinner } from "@/components/kit/feedback"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger, Sheet, SheetContent, SheetTitle } from "@/components/kit/overlays"
import { DropdownMenuGroup, DropdownMenuLabel, DropdownMenuSeparator } from "@/components/ui/dropdown-menu"
import { api, LOCAL, routeMissing } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { cn } from "@/lib/utils"
import { chatShellsQuery } from "./chat-shells"
import { terminalPool } from "./pool"
import { TerminalSession } from "./terminal-session"
import { TerminalScreen } from "./terminal-screen"

const defaultHeight = 260
const heightKey = "agentpack.terminal-panel-height"
const control = "grid h-(--ctl-h-sm) w-(--ctl-h) shrink-0 place-items-center rounded-(--r-ctl) text-(--fg2) hover:bg-(--hover) hover:text-(--fg) disabled:opacity-35 [&_svg]:size-3.5"
type Place = "chat" | "main" | "home"

function shellName(shell: SessionInfo) {
  return shell.title === "Home" ? "~" : basename(shell.directory)
}

function readHeight() {
  try {
    const saved = Number(localStorage.getItem(heightKey))
    if (Number.isFinite(saved) && saved >= 120) return saved
  } catch {
    // Storage is optional.
  }
  return defaultHeight
}

export default function TerminalPanel({ chat, machine, machineName, phone, offline, open, onHide, newShellRequest, shells }: {
  chat: SessionInfo
  machine: string
  machineName: string
  phone: boolean
  offline: boolean
  open: boolean
  onHide: () => void
  newShellRequest: number
  shells: UseQueryResult<SessionInfo[]>
}) {
  const client = useQueryClient()
  const navigate = useNavigate()
  const { queryKey } = chatShellsQuery(machine, chat.id)
  const [selected, setSelected] = useState<string>()
  const [confirm, setConfirm] = useState<SessionInfo>()
  const [height, setHeight] = useState(readHeight)
  const [maximized, setMaximized] = useState(false)
  const drag = useRef<{ y: number; height: number; max: number } | null>(null)
  const handled = useRef({ open: false, request: 0 })
  const rows = shells.data ?? []
  const active = rows.find((shell) => shell.id === selected) ?? rows.at(-1)
  const [view, setView] = useState<{ id: string; terminal: TerminalSession }>()
  const terminal = view && view.id === active?.id ? view.terminal : undefined
  const worktree = Boolean(chat.worktree_of)

  const start = useMutation({
    mutationFn: (place: Place) => api<SessionInfo>("/api/sessions", { machine, body: { transport: "shell", parent_id: chat.id, place } }),
    onSuccess: (shell) => {
      client.setQueryData<SessionInfo[]>(queryKey, (current) => [...(current ?? []).filter((row) => row.id !== shell.id), shell])
      setSelected(shell.id)
    },
  })
  const finish = useMutation({
    mutationFn: ({ shell, promote }: { shell: SessionInfo; promote: boolean }) => api(
      `/api/sessions/${encodeURIComponent(shell.id)}/${promote ? "detach-shell" : "stop"}`,
      { machine, method: "POST" },
    ),
    onSuccess: (_, { shell, promote }) => {
      client.setQueryData<SessionInfo[]>(queryKey, (current) => current?.filter((row) => row.id !== shell.id) ?? [])
      if (promote) {
        void client.invalidateQueries({ queryKey: ["m", LOCAL, "sessions"], exact: true })
        void navigate({ to: "/sessions/$machine/$sessionId", params: { machine, sessionId: shell.id } })
      } else {
        terminalPool.dispose(machine, shell.id)
        if (rows.length === 1 && open) onHide()
      }
    },
  })

  const { mutate: startShell, isPending: starting } = start
  const ready = shells.isSuccess
  const hasShells = rows.length > 0
  useEffect(() => {
    if (!open) {
      handled.current.open = false
      setMaximized(false)
      return
    }
    if (offline || !ready || starting) return
    const requested = newShellRequest !== handled.current.request
    const firstOpen = !handled.current.open
    handled.current = { open: true, request: newShellRequest }
    if (requested || (firstOpen && !hasShells)) startShell("chat")
  }, [open, offline, ready, starting, hasShells, newShellRequest, startShell])

  const activeId = active?.id
  useEffect(() => {
    if (activeId && open) {
      setView({ id: activeId, terminal: terminalPool.get(machine, activeId, () => new TerminalSession(machine, activeId)) })
    }
  }, [machine, activeId, open])

  function close(shell: SessionInfo) {
    // A detached socket still receives worker state. If this browser never attached, confirm.
    const snapshot = terminalPool.peek(machine, shell.id)?.getSnapshot()
    if (snapshot?.shellCommand !== "idle" || snapshot.busy) setConfirm(shell)
    else finish.mutate({ shell, promote: false })
  }

  function resize(value: number) {
    setHeight(value)
    try {
      localStorage.setItem(heightKey, String(value))
    } catch {
      // Storage is optional.
    }
  }

  const error = start.error ?? finish.error ?? shells.error
  const body = (
    <>
      <header className="flex h-[30px] shrink-0 items-center gap-(--s2) border-b-[0.5px] border-(--sep) bg-(--chrome) ps-(--s4) pe-(--s3)">
        <div role="tablist" aria-label="Terminals" className="flex min-w-0 items-center gap-px overflow-x-auto rounded-(--r-ctl)">
          {rows.map((shell) => (
            <ShellTab key={shell.id} shell={shell} machine={machine} terminal={shell.id === active?.id ? terminal : undefined} active={shell.id === active?.id} offline={offline || finish.isPending}
              select={() => { setSelected(shell.id); terminalPool.focus(machine, shell.id) }} close={() => close(shell)} />
          ))}
        </div>
        <Menu>
          <MenuTrigger disabled={offline || starting || !ready} aria-label="New terminal" title="New terminal (Ctrl+Shift+`)"
            className="flex h-(--ctl-h-sm) shrink-0 items-center gap-px rounded-(--r-ctl) ps-(--s2) pe-(--s1) text-(--fg2) hover:bg-(--hover) disabled:opacity-35">
            <Plus className="size-3.5" aria-hidden /><ChevronDown className="size-2.5" aria-hidden />
          </MenuTrigger>
          <MenuContent side="top" className="min-w-72">
            <DropdownMenuGroup>
              <DropdownMenuLabel className="ps-(--s4) text-(length:--fs-xs) text-(--fg3)">New terminal on {machineName}</DropdownMenuLabel>
              <MenuItem icon={<SquareTerminal />} shortcut="⌃⇧`" onClick={() => startShell("chat")}>
                {basename(chat.directory)} <span className="text-(--fg2)">{worktree ? "this chat’s worktree" : "this chat’s folder"}</span>
              </MenuItem>
              <MenuItem icon={<SquareTerminal />} onClick={() => startShell("main")}>
                {basename(chat.worktree_of || chat.directory)} <span className="text-(--fg2)">main folder</span>
              </MenuItem>
              <MenuItem icon={<SquareTerminal />} onClick={() => startShell("home")}>~ <span className="text-(--fg2)">home folder</span></MenuItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <MenuItem disabled={!active || finish.isPending} onClick={() => active && finish.mutate({ shell: active, promote: true })}>Open as session</MenuItem>
          </MenuContent>
        </Menu>
        <span className="flex-1" />
        {active && <span className="min-w-0 truncate text-(length:--fs-xs) text-(--fg2)">
          {active.worktree_of ? `Worktree ${shellName(active)}` : active.title === "Home" ? "Home" : active.directory} · {machineName}
        </span>}
        {!phone && <button type="button" className={control} aria-label={maximized ? "Restore terminal" : "Maximize terminal"} title={maximized ? "Restore terminal" : "Maximize terminal"}
          onClick={() => { setMaximized(!maximized); terminal?.focus() }}>{maximized ? <Minimize2 /> : <Maximize2 />}</button>}
        <button type="button" className={control} aria-label="Hide terminal" title="Hide terminal (⌘J)" onClick={onHide}><ChevronDown /></button>
      </header>
      {offline ? <Banner>{machineName} is offline. Its shells come back when it reconnects.</Banner>
        : error ? <Banner>{routeMissing(error) ? "Update this machine to use chat terminals." : error.message}</Banner>
        : (starting || shells.isPending) && <Banner><Spinner /> Starting terminal…</Banner>}
    </>
  )

  const screen = active && terminal && (
    <PanelScreen key={active.id} terminal={terminal} visible={open} onExit={() => {
      client.setQueryData<SessionInfo[]>(queryKey, (current) => current?.filter((row) => row.id !== active.id) ?? [])
      terminalPool.dispose(machine, active.id)
      if (rows.length === 1 && open) onHide()
    }} />
  )

  return (
    <>
      {phone ? (
        <Sheet open={open} onOpenChange={(next) => !next && open && onHide()}>
          <SheetContent finalFocus={false} className="flex h-[60dvh] max-h-[85dvh] flex-col gap-0 overflow-hidden bg-(--terminal-bg) p-0">
            <SheetTitle className="sr-only">Terminal</SheetTitle>
            {body}
            {screen}
          </SheetContent>
        </Sheet>
      ) : (
        <section aria-label="Terminal" hidden={!open} className={cn("flex min-h-0 shrink-0 flex-col border-t-[0.5px] border-(--sep) bg-(--terminal-bg) [&[hidden]]:hidden",
          maximized && open ? "absolute inset-0 z-30" : "relative max-h-[calc(100%-120px)]")}
          style={maximized && open ? undefined : { height }}>
          {!maximized && <div role="separator" tabIndex={0} aria-orientation="horizontal" aria-label="Resize terminal" aria-valuemin={120} aria-valuenow={height}
            className="absolute -top-[3px] inset-x-0 z-10 h-[6px] cursor-row-resize hover:border-t-2 hover:border-(--accent)"
            onPointerDown={(event) => {
              const panel = event.currentTarget.parentElement
              if (!panel) return
              event.currentTarget.setPointerCapture(event.pointerId)
              drag.current = { y: event.clientY, height: panel.offsetHeight, max: Math.max(120, (panel.parentElement?.clientHeight ?? height) - 160) }
            }}
            onPointerMove={(event) => {
              const from = drag.current
              if (from) resize(Math.max(120, Math.min(from.max, from.height + from.y - event.clientY)))
            }}
            onLostPointerCapture={() => { drag.current = null }}
            onDoubleClick={() => resize(defaultHeight)}
            onKeyDown={(event) => {
              const max = Math.max(120, (event.currentTarget.parentElement?.parentElement?.clientHeight ?? height) - 160)
              if (event.key === "ArrowUp" || event.key === "ArrowDown" || event.key === "Home") {
                event.preventDefault()
                resize(event.key === "Home" ? defaultHeight : Math.max(120, Math.min(max, height + (event.key === "ArrowUp" ? 16 : -16))))
              }
            }} />}
          {body}
          {screen}
        </section>
      )}
      <ConfirmAlert open={!!confirm} onOpenChange={(next) => !next && setConfirm(undefined)}>
        <ConfirmAlertContent title={`Close ${confirm ? shellName(confirm) : ""} terminal?`}
          description="Closing the shell ends any command running in it."
          destructiveLabel="Close terminal" onConfirm={() => {
            if (confirm) finish.mutate({ shell: confirm, promote: false })
            setConfirm(undefined)
          }} />
      </ConfirmAlert>
    </>
  )
}

function ShellTab({ shell, machine, terminal: selectedTerminal, active, offline, select, close }: {
  shell: SessionInfo; machine: string; terminal?: TerminalSession; active: boolean; offline: boolean; select: () => void; close: () => void
}) {
  const terminal = selectedTerminal ?? terminalPool.peek(machine, shell.id)
  const snapshot = useSyncExternalStore(terminal?.subscribe ?? idleSubscribe, terminal?.getSnapshot ?? emptySnapshot)
  const name = shellName(shell)
  return (
    <div className={cn("group flex h-(--ctl-h-sm) max-w-55 shrink-0 items-center rounded-(--r-row) pe-(--s2) text-(length:--fs-sm) text-(--fg2) hover:bg-(--hover)",
      active && "bg-(--control) text-(--fg) shadow-(--shadow-ctl)")}>
      <button type="button" role="tab" aria-selected={active} title={shell.directory} onClick={select}
        className="flex min-w-0 items-center gap-(--s2) whitespace-nowrap ps-(--s3)">
        <SquareTerminal className="size-[13px] shrink-0 opacity-75" aria-hidden /><span className="truncate">{name}</span>
        {snapshot?.shellCommand === "running" ? <span className="size-[6px] shrink-0 rounded-full bg-(--ok)" title="Running command" />
          : shell.worktree_of && <span className="text-(--fg3)">· worktree</span>}
      </button>
      <button type="button" aria-label={`Close ${name} terminal`} disabled={offline} onClick={close}
        className={cn("ms-(--s2) grid size-(--s4) shrink-0 place-items-center rounded-(--r-row) text-(--fg3) hover:bg-(--press) hover:text-(--fg) disabled:opacity-35",
          !active && "opacity-0 focus-visible:opacity-100 group-hover:opacity-100")}><X className="size-2.5" aria-hidden /></button>
    </div>
  )
}

const idleSubscribe = () => () => {}
const emptySnapshot = () => undefined

function PanelScreen({ terminal, visible, onExit }: { terminal: TerminalSession; visible: boolean; onExit: () => void }) {
  const stage = useRef<HTMLDivElement>(null)
  const snapshot = useSyncExternalStore(terminal.subscribe, terminal.getSnapshot)
  useEffect(() => {
    const element = stage.current
    if (!visible || !element) return
    terminal.attach(element, true)
    return () => terminal.detach(element)
  }, [terminal, visible])
  useEffect(() => {
    if (snapshot.ended) onExit()
  }, [snapshot.ended, onExit])
  return (
    <>
      {snapshot.banner && <Banner>{snapshot.banner}</Banner>}
      <TerminalScreen stageRef={stage} busy={snapshot.busy} terminal={terminal} />
    </>
  )
}
