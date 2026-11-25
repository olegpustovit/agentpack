import { lazy, Suspense, useEffect, useRef, useState, type KeyboardEvent, type MouseEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate, useParams, useRouter, useSearch } from "@tanstack/react-router"
import { Folder, Laptop, Plus, Server } from "lucide-react"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { FilterButton, FilterToken } from "@/components/kit/filters"
import { GroupHeader } from "@/components/kit/list"
import { RowContextMenu, RowContextMenuCheckboxItem, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { ContextMenuSeparator } from "@/components/ui/context-menu"
import { buttonVariants } from "@/components/ui/button"
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
import { clearedFilter, filterSearch, filterTokens, matchesFilter, readFilter, type FilterKey, type SessionFilter } from "./filter"
import { groupSessions, nestHelpers, rowKey, treeSessions, type GroupMode, type ListedSession, type SessionSubhead } from "./group"
import { NotifyOffer } from "@/features/shell/attention"
import { loadStartChoices } from "./choices"
import { agentIds, defaultAgent, sessionRules } from "./rules"
import { useListedSessions } from "./queries"
import { useStartSession } from "./start"
import { isUnread, sessionGlyphState, useSeen } from "./session-status"
import { SessionRow, SubagentRow, type GroupActions, type RowActions } from "./session-row"
import { RowSheet, type SheetState } from "./session-sheets"
import { SessionActionAlert } from "./session-action-alert"

// Selection follows the arrow keys at once. The route follows 150 ms after the last press, so
// holding a key opens one terminal. `from` is the route the cursor was set under; a route change
// drops it without an effect.
type Cursor = { key: string; from: string | undefined }
type Confirm = { kind: "stop" | "restart" | "archive"; rows: ListedSession[] }

const SessionView = lazy(() => import("./session-view"))

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

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
  const seen = useSeen()
  const pending = useRef<number | undefined>(undefined)
  // Rows marked with ⌘- or Shift-click, as in Finder; a plain click, an arrow key or Esc clears them.
  const [marked, setMarked] = useState<ReadonlySet<string>>(new Set())
  const anchor = useRef<string | undefined>(undefined)

  const groupMode: GroupMode = search.group === "machine" ? "machine" : "project"
  const routeKey = params.machine && params.sessionId ? `${params.machine}:${params.sessionId}` : undefined
  const selectedKey = cursor?.from === routeKey ? cursor?.key : routeKey

  const filter = readFilter(search)
  const listed = treeSessions(sessions).filter((session) => !archiving.has(rowKey(session)))
  const shown = listed.filter((session) => matchesFilter(session, filter))
  const tokens = filterTokens(filter, (id) => machines.find((machine) => machine.id === id)?.name ?? id)
  const isDefault = tokens.length === 0 && groupMode === "project"
  const [viewOpen, setViewOpen] = useState(false)
  const [viewLoaded, setViewLoaded] = useState(false)
  const viewButton = useRef<HTMLButtonElement>(null)
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

  function openView(open: boolean) {
    if (open) setViewLoaded(true)
    setViewOpen(open)
  }

  // ⌘F opens the view options with the filter field focused; the field lives in the popover.
  useEffect(() => {
    function onFind(event: globalThis.KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "f") {
        event.preventDefault()
        openView(true)
      }
    }
    window.addEventListener("keydown", onFind)
    return () => window.removeEventListener("keydown", onFind)
  }, [])

  function setSearch(patch: Partial<Record<FilterKey | "group", string>>) {
    const current = router.state.location
    // Keep a phone deep link in All sessions after clearing the filter that opened it.
    const next = { ...current.search, ...(isPhone && !routeKey ? { tab: "all" } : {}), ...patch }
    // Preserve search strings such as "123" through the router’s JSON parsing.
    const query = router.options.stringifySearch(next)
    const hash = current.hash ? `#${current.hash}` : ""
    void navigate({ href: `${current.pathname}${query}${hash}`, replace: true })
  }

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
    // ⌘⌫ archives the marked rows, or the focused one, as Finder moves to Trash. Confirms only when it would interrupt an agent.
    if ((event.key === "Backspace" || event.key === "Delete") && (isMac ? event.metaKey : event.ctrlKey)) {
      const targets = (marked.size > 0 ? markedRows : rows[current] ? [rows[current]] : []).filter((row) => sessionRules(row).archive)
      if (targets.length === 0) return
      event.preventDefault()
      setMarked(new Set())
      requestArchive(targets)
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
    archive: () => { setMarked(new Set()); requestArchive(archivable) },
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

  // Archiving stops busy agents first, so that case is reviewed (SES-03); idle rows archive at once.
  function requestArchive(targets: ListedSession[]) {
    if (targets.some((row) => sessionRules(row).busy)) setConfirm({ kind: "archive", rows: targets })
    else archiveRows(targets)
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
      archive: () => requestArchive([row]),
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
      projectSettings: () => setSheet({ kind: "project", row }),
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

  const filtered = tokens.length > 0
  const viewLabel = filtered ? `View options, ${plural(tokens.length, "filter")} on` : "View options"
  const viewTrigger = (
    <FilterButton ref={viewButton} active={filtered} aria-label={viewLabel} title={`${viewLabel} (${isMac ? "⌘F" : "Ctrl+F"})`}
      aria-expanded={viewOpen} onClick={() => openView(!viewOpen)} />
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      {!routeKey && <ToolbarTitle title="Sessions" subtitle={`${plural(sessions.length, "session")} on ${plural(machines.length, "machine")}`} />}
      {isPhone && <ToolbarSlot>{viewTrigger}<NewSessionLink /></ToolbarSlot>}
      {!isPhone && (
        // One row: the Work link fills it and the view and New buttons sit on its trailing edge.
        <div className="shrink-0 px-(--s2) pt-(--s3) pb-(--s2)">
          <div className="relative">
            <Link to="/sessions" search={{ q: search.q, group: search.group, agent: search.agent, status: search.status, host: search.host, tab: "work" }} activeOptions={{ exact: true, includeSearch: false }}
              className="flex min-h-(--row-h) items-center rounded-(--r-row) ps-(--s3) pe-[calc(2*var(--ctl-h)+var(--s2))] font-medium hover:bg-(--hover) aria-[current=page]:bg-(--page) aria-[current=page]:shadow-(--e-ctl)">
              Work
            </Link>
            <div className="absolute inset-y-0 end-(--s1) flex items-center">
              {viewTrigger}
              <NewSessionLink />
            </div>
          </div>
        </div>
      )}
      {filtered && (
        <div role="group" aria-label="Active filters" className="flex shrink-0 flex-wrap items-center gap-(--s2) px-(--s4) pb-(--s3) max-[760px]:pt-(--s3)">
          {tokens.map((token) => <FilterToken key={token.key} label={token.label} value={token.value} onOpen={() => openView(true)} onRemove={() => setSearch({ [token.key]: undefined })} />)}
          <span className="ms-auto flex items-center gap-(--s2) text-(length:--fs-xs) text-(--fg2) tabular-nums">
            {`${shown.length} of ${listed.length}`}
            <button type="button" onClick={() => setSearch(clearedFilter)} className="rounded-(--r-xs) font-medium text-(--accent) hover:underline">Clear</button>
          </span>
        </div>
      )}
      {viewLoaded && (
        <Suspense fallback={null}>
          <SessionView anchor={viewButton} open={viewOpen} onOpenChange={openView}
            filter={filter} group={groupMode} sessions={listed} machines={machines} isDefault={isDefault}
            onFilter={(patch: Partial<SessionFilter>) => setSearch(filterSearch(patch))}
            onGroup={(group) => setSearch({ group: group === "project" ? undefined : group })}
            onReset={() => setSearch({ ...clearedFilter, group: undefined })}
            refresh={{ pending: refresh.isPending, run: () => refresh.mutate() }} />
        </Suspense>
      )}

      <nav
        aria-label="Sessions"
        className="group/list flex min-h-0 flex-1 flex-col overflow-y-auto px-(--s2) pb-(--s2)"
        onKeyDown={onKeyDown}
        onClick={onClick}
        onContextMenu={onContextMenu}
      >
        {discovery.error && <Banner tone="warn">{`Can't look for other machines: ${discovery.error.message}`}</Banner>}
        {machines.map((machine) => {
          const error = !outdated.includes(machine) && queries.errors[machine.id]
          return error ? <Banner key={machine.id} tone="danger">{`Couldn't load sessions from ${machine.name}: ${error.message}`}</Banner> : null
        })}
        {loading && [0, 1, 2].map((index) => <RowSkeleton key={index} />)}
        {!loading && !failed && rows.length === 0 && (filtered ? (
          <Empty title="No matching sessions">
            {`${plural(listed.length, "session")} hidden by filters. `}
            <button type="button" onClick={() => setSearch(clearedFilter)} className="font-medium text-(--accent) hover:underline">Clear filters</button>
          </Empty>
        ) : (
          <Empty title="No sessions yet">
            Start an agent with New session, or from the CLI on a machine.
          </Empty>
        ))}
        {needsYou.length > 0 && (
          <div>
            <GroupHeader>
              <span className="min-w-0 flex-1 truncate">{`Needs you · ${needsYou.length}`}</span>
              <NotifyOffer />
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
      {confirm && (
        <SessionActionAlert
          {...confirm}
          onClose={() => setConfirm(null)}
          onConfirm={() => {
            const { kind, rows: targets } = confirm
            setConfirm(null)
            setMarked(new Set())
            if (kind === "stop") stopRows(targets)
            else if (kind === "archive") archiveRows(targets)
            else for (const row of targets) void commands.restart(row)
          }}
        />
      )}
    </div>
  )
}

export function NewSessionLink() {
  return (
    <Link
      to="."
      search={(prev: Record<string, unknown>) => ({ ...prev, new: "1", machine: undefined, folder: undefined })}
      replace
      aria-label="New session"
      title="New session"
      className={buttonVariants({ variant: "ghost", size: "icon" })}
    >
      <Plus aria-hidden />
    </Link>
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

