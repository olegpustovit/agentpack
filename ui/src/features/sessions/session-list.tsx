import { useEffect, useRef, useState, type KeyboardEvent, type MouseEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate, useParams, useRouter, useSearch } from "@tanstack/react-router"
import { Folder, Laptop, Plus, RefreshCw, Server } from "lucide-react"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { GroupHeader, SearchField, SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { ConfirmAlert, ConfirmAlertContent, RowContextMenu, RowContextMenuCheckboxItem, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { ContextMenuSeparator } from "@/components/ui/context-menu"
import { Button, buttonVariants } from "@/components/ui/button"
import { useEventsConnected } from "@/features/events/use-events"
import { nodesQuery } from "@/features/machines/queries"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { terminalPool } from "@/features/terminal/pool"
import { rememberSession } from "@/features/shell/views"
import { LOCAL } from "@/lib/api"
import { basename, harnessName, plural } from "@/lib/format"
import { cn } from "@/lib/utils"
import { useSessionCommands } from "./actions"
import { groupSessions, matchesSession, nestHelpers, rowKey, sessionLabel, treeSessions, type GroupMode, type ListedSession, type SessionSubhead } from "./group"
import { NotifyOffer } from "@/features/shell/attention"
import { NewSessionSheet } from "./new-session-sheet"
import { loadStartChoices } from "./choices"
import { agentIds, defaultAgent, sessionRules } from "./rules"
import { useListedSessions } from "./queries"
import { useStartSession } from "./start"
import { isUnread, loadSeen, saveSeen, sessionGlyphState } from "./session-status"
import { SessionRow, SubagentRow, type GroupActions, type RowActions } from "./session-row"
import { RowSheet, type SheetState } from "./session-sheets"

// Selection follows the arrow keys at once. The route follows 150 ms after the last press, so
// holding a key opens one terminal. `from` is the route the cursor was set under; a route change
// drops it without an effect.
type Cursor = { key: string; from: string | undefined }
type Confirm = { kind: "stop" | "restart"; rows: ListedSession[] }

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

// Computers that could run a gateway. Phones and TVs on the tailnet never will, so they are not missing.

// Rewrites query keys on a full href and keeps the path, hash and every other key.
function withSearch(href: string, patch: Record<string, string | undefined>): string {
  const url = new URL(href, "http://gateway.local")
  for (const [key, value] of Object.entries(patch)) {
    if (value === undefined) url.searchParams.delete(key)
    else url.searchParams.set(key, value)
  }
  return `${url.pathname}${url.search}${url.hash}`
}

export function SessionList() {
  const { machines, queries, sessions } = useListedSessions()
  const live = useEventsConnected()
  const discovery = useQuery(nodesQuery(live))
  const queryClient = useQueryClient()
  const refresh = useMutation({
    mutationFn: () => Promise.all([["nodes"], ["topology"], ["m"]].map((queryKey) => queryClient.invalidateQueries({ queryKey }))),
  })
  const search = useSearch({ from: "/_app/sessions" })
  const openSub = typeof (search as { sub?: unknown }).sub === "string" ? (search as { sub: string }).sub : undefined
  const params = useParams({ strict: false })
  const navigate = useNavigate()
  // Search-only changes keep the current path (a session may be open), so they are built as hrefs.
  // Read when a handler runs, so the list does not re-render on every URL change.
  const router = useRouter()
  const [sheet, setSheet] = useState<SheetState | null>(null)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [stopping, setStopping] = useState<ReadonlySet<string>>(new Set())
  const [archiving, setArchiving] = useState<ReadonlySet<string>>(new Set())
  const commands = useSessionCommands((session, pending) => {
    const key = rowKey(session)
    setArchiving((current) => {
      const next = new Set(current)
      if (pending) next.add(key)
      else next.delete(key)
      return next
    })
  })
  const [cursor, setCursor] = useState<Cursor | null>(null)
  const [seen, setSeen] = useState(loadSeen)
  const pending = useRef<number | undefined>(undefined)
  // Rows marked with ⌘- or Shift-click, as in Finder; a plain click, an arrow key or Esc clears them.
  const [marked, setMarked] = useState<ReadonlySet<string>>(new Set())
  const anchor = useRef<string | undefined>(undefined)

  const groupMode: GroupMode = search.group === "machine" ? "machine" : "project"
  const routeKey = params.machine && params.sessionId ? `${params.machine}:${params.sessionId}` : undefined
  const selectedKey = cursor?.from === routeKey ? cursor?.key : routeKey

  const query = search.q ?? ""
  const shown = treeSessions(sessions).filter((session) => !archiving.has(rowKey(session)) && matchesSession(session, query))
  // Pinned above the machine and project groups, and listed only there.
  const needsYou = shown.filter((session) => sessionGlyphState(session) === "needsYou")
  const pinned = new Set(needsYou)
  const groups = groupSessions(shown.filter((session) => !pinned.has(session)), groupMode)
  const isPhone = useIsPhone()
  // Flat order matches the DOM order, so keyboard index and element index agree.
  const rows = needsYou.concat(groups.flatMap((group) => group.subheads.flatMap((head) => nestHelpers(head.sessions).map((entry) => entry.session))))
  const first = rows[0]
  const tabStop = selectedKey ?? (first ? rowKey(first) : undefined)
  const outdated = machines.filter((machine) => machine.needsUpdate || queries.byMachine[machine.id]?.needsUpdate)
  const loading = queries.firstLoadPending
  // A failed read shows its banner only; the empty copy is for a successful empty list.
  const failed = Object.keys(queries.errors).length > 0

  useEffect(() => () => window.clearTimeout(pending.current), [])

  // Opening a row marks it seen; activity that lands while it stays open counts too.
  const openUsed = rows.find((row) => rowKey(row) === routeKey)?.last_used
  useEffect(() => {
    if (!routeKey) return
    const at = Math.max(Date.now(), openUsed ? Date.parse(openUsed) || 0 : 0)
    setSeen((current) => {
      const next = { ...current, [routeKey]: at }
      saveSeen(next)
      return next
    })
  }, [routeKey, openUsed])

  function openRow(row: ListedSession, replace = false) {
    void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: row.machine, sessionId: row.id }, search: true, replace })
  }

  function onKeyDown(event: KeyboardEvent<HTMLElement>) {
    const elements = Array.from(event.currentTarget.querySelectorAll<HTMLElement>("[data-source-row]:not([data-subagent-row])"))
    const current = elements.indexOf(event.target as HTMLElement)
    if (event.key === "Enter" && rows[current]) {
      // Enter is the deliberate move into the terminal: blur first, so attaching takes focus too.
      const row = rows[current]
      event.preventDefault()
      window.clearTimeout(pending.current)
      ;(event.target as HTMLElement).blur()
      openRow(row)
      terminalPool.focus(row.machine, row.id)
      return
    }
    if (event.key === "F2") {
      const row = rows[current]
      if (row && sessionRules(row).rename) setSheet({ kind: "rename", row })
      return
    }
    // ⌘⌫ archives the marked rows, or the focused one, as Finder moves to Trash. No confirmation.
    if ((event.key === "Backspace" || event.key === "Delete") && (isMac ? event.metaKey : event.ctrlKey)) {
      const targets = (marked.size > 0 ? markedRows : rows[current] ? [rows[current]] : []).filter((row) => sessionRules(row).archive)
      if (targets.length === 0) return
      event.preventDefault()
      setMarked(new Set())
      archiveRows(targets)
      return
    }
    if (event.key === "Escape" && marked.size > 0) {
      event.preventDefault()
      setMarked(new Set())
      return
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return
    event.preventDefault()
    setMarked(new Set())
    const next = Math.min(Math.max(current + (event.key === "ArrowDown" ? 1 : -1), 0), elements.length - 1)
    const row = rows[next]
    const element = elements[next]
    if (!row || !element) return
    element.focus()
    setCursor({ key: rowKey(row), from: routeKey })
    window.clearTimeout(pending.current)
    // A single press opens at once; a held key opens only where it stops, so rows passed over never dial a terminal.
    // Moving between open sessions replaces the history entry, so Back leaves the list instead of retracing it.
    // History updates as navigate is called; routeKey still holds the previous render's value during fast key presses.
    const replace = /^\/sessions\/[^/]+\/[^/]+/.test(router.history.location.pathname)
    if (event.repeat) pending.current = window.setTimeout(() => openRow(row, replace), 150)
    else openRow(row, replace)
  }

  // ⌘-click toggles a row, Shift-click marks the range from the last one; neither opens it.
  // Runs after the row link's own handler, which leaves modified clicks to the browser.
  function onClick(event: MouseEvent<HTMLElement>) {
    const key = (event.target as HTMLElement).closest<HTMLElement>("[data-row-key]")?.dataset.rowKey
    if (!key) return
    // Control-click is the context menu on a Mac, so ⌘ toggles there and Ctrl elsewhere.
    const toggle = isMac ? event.metaKey : event.ctrlKey
    if (!toggle && !event.shiftKey) {
      anchor.current = key
      if (marked.size > 0) setMarked(new Set())
      return
    }
    event.preventDefault()
    const keys = rows.map(rowKey)
    const next = new Set(marked.size > 0 ? marked : selectedKey ? [selectedKey] : [])
    const from = keys.indexOf(anchor.current ?? selectedKey ?? key)
    if (event.shiftKey && from >= 0) {
      const to = keys.indexOf(key)
      for (const each of keys.slice(Math.min(from, to), Math.max(from, to) + 1)) next.add(each)
    } else {
      if (next.has(key)) next.delete(key)
      else next.add(key)
      anchor.current = key
    }
    setMarked(next)
  }

  // Finder: right-clicking a row outside the marked set acts on that row alone.
  function onContextMenu(event: MouseEvent<HTMLElement>) {
    const key = (event.target as HTMLElement).closest<HTMLElement>("[data-row-key]")?.dataset.rowKey
    if (key && marked.size > 0 && !marked.has(key)) setMarked(new Set())
  }

  const markedRows = rows.filter((row) => marked.has(rowKey(row)))
  const stoppable = markedRows.filter((row) => sessionRules(row).stop)
  const archivable = markedRows.filter((row) => sessionRules(row).archive)
  const group: GroupActions | undefined = markedRows.length > 1 ? {
    stopCount: stoppable.length,
    archiveCount: archivable.length,
    stop: () => setConfirm({ kind: "stop", rows: stoppable }),
    archive: () => { setMarked(new Set()); archiveRows(archivable) },
    ids: markedRows.map((row) => row.id),
  } : undefined

  function stopRows(targets: ListedSession[]) {
    const keys = targets.map(rowKey)
    // An agent that ignores the signal can hold this request for up to 90 s.
    setStopping((current) => new Set([...current, ...keys]))
    void Promise.all(targets.map((row) => commands.stop(row))).finally(() =>
      setStopping((current) => new Set([...current].filter((key) => !keys.includes(key)))),
    )
  }

  function archiveRows(targets: ListedSession[]) {
    const keys = new Set(targets.map(rowKey))
    if (routeKey && keys.has(routeKey)) {
      const index = rows.findIndex((row) => rowKey(row) === routeKey)
      const next = rows.slice(index + 1).find((row) => !keys.has(rowKey(row)))
        ?? rows.slice(0, index).findLast((row) => !keys.has(rowKey(row)))
      if (next) openRow(next, true)
      else void navigate({ to: "/sessions", search: true, replace: true })
    }
    for (const row of targets) commands.archive(row)
  }

  function actionsFor(row: ListedSession, selected: boolean): RowActions {
    return {
      rename: () => setSheet({ kind: "rename", row }),
      restart: () => setConfirm({ kind: "restart", rows: [row] }),
      resume: () => {
        void commands.resume(row)
        if (!selected) openRow(row)
      },
      stop: () => setConfirm({ kind: "stop", rows: [row] }),
      archive: () => archiveRows([row]),
      // A pooled session on the open row must dial again; navigating to the row it already shows does nothing.
      reconnect: () => {
        terminalPool.reconnect(row.machine, row.id)
        if (!selected) openRow(row)
      },
      closeTerminal: () => {
        // The socket stays open for a pooled session, so closing must also end it.
        if (selected) void navigate({ to: "/sessions", search: true }).then(() => {
          rememberSession(undefined)
          terminalPool.dispose(row.machine, row.id)
        })
      },
      install: () => setSheet({ kind: "install", row }),
      save: () => setSheet({ kind: "save", row }),
      tools: () => void commands.setTools(row, !row.tools),
    }
  }

  function renderRow(session: ListedSession, nested = false) {
    const key = rowKey(session)
    const selected = key === selectedKey
    // Running subagents show under their chat on every width; treeSessions drops finished ones.
    const children = session.offline || session.archived_at ? [] : session.subagents ?? []
    return (
      <div key={key}>
        <SessionRow
          session={session}
          selected={selected && !openSub}
          tabStop={key === tabStop}
          stopping={stopping.has(key)}
          unread={isUnread(session, seen[key] ?? seen[""])}
          actions={actionsFor(session, selected)}
          marked={marked.has(key)}
          group={marked.has(key) ? group : undefined}
          nested={nested}
        />
        {children.map((child) => (
          <SubagentRow key={child.id} session={session} subagent={child} selected={selected && openSub === child.id} />
        ))}
      </div>
    )
  }

  function chooseGroup(value: unknown[]) {
    const next = value[0]
    if (next !== "machine" && next !== "project") return
    void navigate({ href: withSearch(router.state.location.href, { group: next }), replace: true })
  }

  function closeNewSession(open: boolean) {
    if (!open) void navigate({ href: withSearch(router.state.location.href, { new: undefined, machine: undefined, folder: undefined }), replace: true })
  }

  const newSession = (
    <Link
      to="."
      search={(prev: Record<string, unknown>) => ({ ...prev, new: "1", machine: undefined, folder: undefined })}
      aria-label="New session"
      title="New session"
      className={cn(buttonVariants({ variant: "ghost", size: "icon" }))}
    >
      <Plus aria-hidden />
    </Link>
  )

  const refreshButton = (
    <Button variant="ghost" size="icon" aria-label="Refresh sessions" title="Refresh sessions" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
      <RefreshCw aria-hidden className={cn(refresh.isPending && "animate-spin motion-reduce:animate-none")} />
    </Button>
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      {!routeKey && <ToolbarTitle title="Sessions" subtitle={`${plural(sessions.length, "session")} on ${plural(machines.length, "machine")}`} />}
      {isPhone && <ToolbarSlot>{refreshButton}{newSession}</ToolbarSlot>}
      <div className="flex shrink-0 flex-col gap-(--s3) p-(--s3)">
        <div className="flex items-center gap-(--s2)">
          <SearchField
            aria-label="Filter sessions"
            placeholder="Filter sessions"
            defaultValue={query}
            onChange={(event) => void navigate({ href: withSearch(router.state.location.href, { q: event.target.value || undefined }), replace: true })}
          />
          {!isPhone && refreshButton}
          {!isPhone && newSession}
        </div>
        <SegmentedControl value={[groupMode]} onValueChange={chooseGroup} aria-label="Group sessions by" className="w-full">
          <SegmentedControlItem value="machine" className="flex-1">Machine</SegmentedControlItem>
          <SegmentedControlItem value="project" className="flex-1">Project</SegmentedControlItem>
        </SegmentedControl>
      </div>

      <nav
        aria-label="Sessions"
        className="group/list flex min-h-0 flex-1 flex-col overflow-y-auto px-(--s2) pb-(--s2)"
        onKeyDown={onKeyDown}
        onClick={onClick}
        onContextMenu={onContextMenu}
      >
        <NotifyOffer />
        {discovery.error && <Banner tone="warn">{`Can't look for other machines: ${discovery.error.message}`}</Banner>}
        {machines.map((machine) => {
          const error = !outdated.includes(machine) && queries.errors[machine.id]
          return error ? <Banner key={machine.id} tone="danger">{`Couldn't load sessions from ${machine.name}: ${error.message}`}</Banner> : null
        })}
        {loading && [0, 1, 2].map((index) => <RowSkeleton key={index} />)}
        {!loading && !failed && rows.length === 0 && (query ? (
          <Empty title="No matches">Try another name, folder or machine.</Empty>
        ) : (
          <Empty title="No sessions yet">
            Start an agent with New session, or from the CLI on a machine.
          </Empty>
        ))}
        {needsYou.length > 0 && (
          <div>
            <GroupHeader>
              <span className="min-w-0 flex-1 truncate">{`Needs you · ${needsYou.length}`}</span>
            </GroupHeader>
            {needsYou.map((session) => renderRow(session))}
          </div>
        )}
        {groups.map((group) => {
          // One folder on one machine needs no second header row: its label and "+" join the group's.
          const only = group.subheads.length === 1 ? group.subheads[0] : undefined
          return (
            <div key={group.key}>
              <GroupHeader className={cn(only && "group/head")}>
                {groupMode === "machine" ? group.key === LOCAL ? <Laptop aria-hidden /> : <Server aria-hidden /> : <Folder aria-hidden />}
                <span className="min-w-0 truncate">{group.label}</span>
                {only && <span className="min-w-0 truncate font-normal text-(--fg3)">{`· ${only.label}`}</span>}
                <span className="flex-1" />
                {groupMode === "machine" && <span className="shrink-0 font-normal text-(--fg3)">{group.offline ? "Offline" : "Online"}</span>}
                {only && <NewInFolder head={only} />}
              </GroupHeader>
              {only && nestHelpers(only.sessions).map(({ session, nested }) => renderRow(session, nested))}
              {!only && group.subheads.map((head) => (
                <div key={head.key}>
                  <GroupHeader className="group/head ps-(--s5) font-normal">
                    {groupMode === "machine" ? <Folder aria-hidden /> : <Server aria-hidden />}
                    <span className="min-w-0 truncate">{head.label}</span>
                    {head.parent && <span className="ms-(--s2) shrink-0 text-(--fg3)">{head.parent}</span>}
                    <NewInFolder head={head} />
                  </GroupHeader>
                  {nestHelpers(head.sessions).map(({ session, nested }) => renderRow(session, nested))}
                </div>
              ))}
            </div>
          )
        })}
      </nav>

      <RowSheet sheet={sheet} onClose={() => setSheet(null)} />
      <ConfirmAlert open={confirm !== null} onOpenChange={(open) => { if (!open) setConfirm(null) }}>
        {confirm && <SessionConfirm
          confirm={confirm}
          onConfirm={() => {
            const { kind, rows: targets } = confirm
            setConfirm(null)
            setMarked(new Set())
            if (kind === "stop") stopRows(targets)
            else for (const row of targets) void commands.restart(row)
          }}
        />}
      </ConfirmAlert>
      <NewSessionSheet open={search.new === "1"} initialMachine={search.machine} initialDirectory={search.folder} onOpenChange={closeNewSession} />
    </div>
  )
}

function SessionConfirm({ confirm, onConfirm }: { confirm: Confirm; onConfirm: () => void }) {
  const { kind, rows } = confirm
  const verb = { stop: "Stop", restart: "Restart" }[kind]
  const row = rows[0]
  if (rows.length > 1 || !row) {
    const machines = new Set(rows.map((each) => each.machineName)).size
    return (
      <ConfirmAlertContent
        title={`${verb} ${plural(rows.length, "agent")}?`}
        description={`On ${plural(machines, "machine")}. The sessions stay in the list so you can resume them; use Archive to remove them.`}
        destructiveLabel={`${verb} ${rows.length} agents`}
        onConfirm={onConfirm}
      />
    )
  }
  return (
    <ConfirmAlertContent
      title={`${verb} ${sessionLabel(row)} in ${basename(row.directory) || "this folder"}?`}
      description={
        kind === "stop"
          ? `On ${row.machineName}. The session stays in the list so you can resume it; use Archive to remove it.`
          : `On ${row.machineName}. Its current conversation may end unless the agent supports resume.`
      }
      destructiveLabel={{ stop: "Stop agent", restart: "Restart agent" }[kind]}
      onConfirm={onConfirm}
    />
  )
}

// "+" starts a chat in this subhead's folder at once with its last agent; ⌥-click or right-click picks
// another, optionally in a new worktree (off each time the menu opens). Revealed on hover; always shown on touch. ⌘N still opens the full New session sheet.
function NewInFolder({ head }: { head: SessionSubhead }) {
  const folder = basename(head.directory) || head.directory
  const machineName = head.sessions[0]?.machineName ?? head.machine
  const usual = defaultAgent(loadStartChoices(head.directory), head.sessions)
  const mutation = useStartSession()
  const [worktree, setWorktree] = useState(false)
  const start = {
    isPending: mutation.isPending,
    mutate: (agent: { harness: string; mode: string }) =>
      mutation.mutate(
        { machine: head.machine, directory: head.directory, ...agent, view: "chat", ...(worktree && { worktree: "new" }) },
      ),
  }
  const label = head.offline ? `${machineName} is offline` : `New ${harnessName(usual.harness)} chat in ${folder} on ${machineName}`
  const className = cn(
    buttonVariants({ variant: "ghost", size: "icon" }),
    "ms-auto size-(--head-h) shrink-0 opacity-0 group-hover/head:opacity-100 focus-visible:opacity-100 pointer-coarse:opacity-100",
  )
  if (head.offline) {
    return (
      <span aria-disabled="true" role="button" aria-label={label} title={label} className={cn(className, "text-(--fg3)")}>
        <Plus aria-hidden />
      </span>
    )
  }
  function onClick(event: MouseEvent<HTMLButtonElement>) {
    if (event.altKey) {
      // The agent list is the context menu; ⌥-click opens it where the pointer is.
      event.currentTarget.dispatchEvent(new globalThis.MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: event.clientX, clientY: event.clientY }))
      return
    }
    if (!start.isPending) start.mutate(usual)
  }
  return (
    <RowContextMenu onOpenChange={(open) => { if (!open) setWorktree(false) }}>
      <RowContextMenuTrigger render={<button type="button" aria-label={label} title={label} className={className} onClick={onClick} />}>
        <Plus aria-hidden />
      </RowContextMenuTrigger>
      <RowContextMenuContent>
        {agentIds.map((id) => (
          <RowContextMenuItem key={id} onClick={() => start.mutate({ harness: id, mode: id === usual.harness ? usual.mode : head.sessions.find((session) => session.harness === id)?.mode || "default" })}>
            {`New ${harnessName(id)} chat`}
          </RowContextMenuItem>
        ))}
        <ContextMenuSeparator />
        <RowContextMenuCheckboxItem checked={worktree} onCheckedChange={setWorktree}>Run in worktree</RowContextMenuCheckboxItem>
      </RowContextMenuContent>
    </RowContextMenu>
  )
}
