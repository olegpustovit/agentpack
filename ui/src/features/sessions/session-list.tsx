import { useEffect, useRef, useState, type KeyboardEvent } from "react"
import { Link, useNavigate, useParams, useRouterState, useSearch } from "@tanstack/react-router"
import { Folder, Laptop, Plus, Server } from "lucide-react"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { GroupHeader, SearchField, SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { buttonVariants } from "@/components/ui/button"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { terminalPool } from "@/features/terminal/pool"
import { LOCAL } from "@/lib/api"
import { basename, plural } from "@/lib/format"
import { cn } from "@/lib/utils"
import { useSessionCommands } from "./actions"
import { groupSessions, matchesSession, rowKey, sessionLabel, type GroupMode, type ListedSession } from "./group"
import { NewSessionSheet } from "./new-session-sheet"
import { sessionRules } from "./rules"
import { sessionsQuery } from "./queries"
import { SessionRow, type RowActions } from "./session-row"
import { RowSheet, type SheetState } from "./session-sheets"

// Selection follows the arrow keys at once. The route follows 150 ms after the last press, so
// holding a key opens one terminal. `from` is the route the cursor was set under; a route change
// drops it without an effect.
type Cursor = { key: string; from: string | undefined }
type Confirm = { kind: "stop" | "restart"; row: ListedSession }

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
  const machines = useMachines()
  const queries = useMachineQueries(sessionsQuery)
  const search = useSearch({ from: "/_app/sessions" })
  const params = useParams({ strict: false })
  const navigate = useNavigate()
  // Search-only changes keep the current path (a session may be open), so they are built as hrefs.
  const href = useRouterState({ select: (state) => state.location.href })
  const commands = useSessionCommands()
  const [sheet, setSheet] = useState<SheetState | null>(null)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [cursor, setCursor] = useState<Cursor | null>(null)
  const pending = useRef<number | undefined>(undefined)

  const groupMode: GroupMode = search.group === "project" ? "project" : "machine"
  const routeKey = params.machine && params.sessionId ? `${params.machine}:${params.sessionId}` : undefined
  const selectedKey = cursor?.from === routeKey ? cursor?.key : routeKey

  const sessions: ListedSession[] = machines.flatMap((machine) =>
    (queries.byMachine[machine.id]?.data ?? []).map((session) => ({
      ...session,
      machine: machine.id,
      machineName: machine.name,
      offline: !machine.online,
    })),
  )
  const query = search.q ?? ""
  const groups = groupSessions(sessions.filter((session) => matchesSession(session, query)), groupMode)
  const isPhone = useIsPhone()
  // Flat order matches the DOM order, so keyboard index and element index agree.
  const rows = groups.flatMap((group) => group.subheads.flatMap((head) => head.sessions))
  const first = rows[0]
  const tabStop = selectedKey ?? (first ? rowKey(first) : undefined)
  const offlineWithData = machines.filter((machine) => !machine.online && queries.byMachine[machine.id]?.data)
  const loading = queries.firstLoadPending
  // A failed read shows its banner only; the empty copy is for a successful empty list.
  const failed = Object.keys(queries.errors).length > 0

  useEffect(() => () => window.clearTimeout(pending.current), [])

  function openRow(row: ListedSession) {
    void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: row.machine, sessionId: row.id }, search: true })
  }

  function onKeyDown(event: KeyboardEvent<HTMLElement>) {
    const elements = Array.from(event.currentTarget.querySelectorAll<HTMLElement>("[data-source-row]"))
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
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return
    event.preventDefault()
    const next = Math.min(Math.max(current + (event.key === "ArrowDown" ? 1 : -1), 0), elements.length - 1)
    const row = rows[next]
    const element = elements[next]
    if (!row || !element) return
    element.focus()
    setCursor({ key: rowKey(row), from: routeKey })
    window.clearTimeout(pending.current)
    pending.current = window.setTimeout(() => openRow(row), 150)
  }

  function actionsFor(row: ListedSession, selected: boolean): RowActions {
    return {
      rename: () => setSheet({ kind: "rename", row }),
      restart: () => setConfirm({ kind: "restart", row }),
      stop: () => setConfirm({ kind: "stop", row }),
      // A pooled session on the open row must dial again; navigating to the row it already shows does nothing.
      reconnect: () => {
        terminalPool.reconnect(row.machine, row.id)
        if (!selected) openRow(row)
      },
      closeTerminal: () => {
        // The socket stays open for a pooled session, so closing must also end it.
        if (selected) void navigate({ to: "/sessions", search: true }).then(() => terminalPool.dispose(row.machine, row.id))
      },
      install: () => setSheet({ kind: "install", row }),
      save: () => setSheet({ kind: "save", row }),
    }
  }

  function chooseGroup(value: unknown[]) {
    const next = value[0]
    if (next !== "machine" && next !== "project") return
    void navigate({ href: withSearch(href, { group: next }), replace: true })
  }

  function closeNewSession(open: boolean) {
    if (!open) void navigate({ href: withSearch(href, { new: undefined }), replace: true })
  }

  const newSession = (
    <Link
      to="."
      search={(prev: Record<string, unknown>) => ({ ...prev, new: "1" })}
      aria-label="New session"
      title="New session"
      className={cn(buttonVariants({ variant: "ghost", size: "icon" }))}
    >
      <Plus aria-hidden />
    </Link>
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      {!routeKey && <ToolbarTitle title="Sessions" subtitle={`${plural(sessions.length, "session")} on ${plural(machines.length, "machine")}`} />}
      {isPhone && <ToolbarSlot>{newSession}</ToolbarSlot>}
      <div className="flex shrink-0 flex-col gap-(--s3) p-(--s3)">
        <div className="flex items-center gap-(--s2)">
          <SearchField
            aria-label="Filter sessions"
            placeholder="Filter sessions"
            defaultValue={query}
            onChange={(event) => void navigate({ href: withSearch(href, { q: event.target.value || undefined }), replace: true })}
          />
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
      >
        {machines.map((machine) => {
          const entry = queries.byMachine[machine.id]
          if (entry?.needsUpdate) {
            return <Banner key={machine.id} tone="warn">{`${machine.name} needs an update to show its sessions.`}</Banner>
          }
          const error = queries.errors[machine.id]
          if (error) {
            return <Banner key={machine.id} tone="danger">{`Couldn't load sessions from ${machine.name}: ${error.message}`}</Banner>
          }
          return null
        })}
        {offlineWithData.length > 0 && (
          <Banner>
            {`${offlineWithData.map((machine) => machine.name).join(", ")} ${offlineWithData.length === 1 ? "is" : "are"} offline. Showing last known sessions.`}
          </Banner>
        )}
        {loading && [0, 1, 2].map((index) => <RowSkeleton key={index} />)}
        {!loading && !failed && rows.length === 0 && (query ? (
          <Empty title="No matches">Try another name, folder or machine.</Empty>
        ) : (
          <Empty title="No sessions yet">
            Start an agent with New session, or from the CLI on a machine.
          </Empty>
        ))}
        {groups.map((group) => (
          <div key={group.key}>
            <GroupHeader>
              {groupMode === "machine" ? group.key === LOCAL ? <Laptop aria-hidden /> : <Server aria-hidden /> : <Folder aria-hidden />}
              <span className="min-w-0 flex-1 truncate">{group.label}</span>
              {groupMode === "machine" && <span className="shrink-0 font-normal text-(--fg3)">{group.offline ? "Offline" : "Online"}</span>}
            </GroupHeader>
            {group.subheads.map((head) => (
              <div key={head.key}>
                <GroupHeader className="ps-(--s5) font-normal">
                  {groupMode === "machine" ? <Folder aria-hidden /> : <Server aria-hidden />}
                  <span className="min-w-0 truncate">{head.label}</span>
                  {head.parent && <span className="ms-(--s2) shrink-0 text-(--fg3)">{head.parent}</span>}
                </GroupHeader>
                {head.sessions.map((session) => {
                  const key = rowKey(session)
                  const selected = key === selectedKey
                  return (
                    <SessionRow
                      key={key}
                      session={session}
                      selected={selected}
                      tabStop={key === tabStop}
                      actions={actionsFor(session, selected)}
                    />
                  )
                })}
              </div>
            ))}
          </div>
        ))}
      </nav>

      <RowSheet sheet={sheet} onClose={() => setSheet(null)} />
      <ConfirmAlert open={confirm !== null} onOpenChange={(open) => { if (!open) setConfirm(null) }}>
        {confirm && (
          <ConfirmAlertContent
            title={`${confirm.kind === "stop" ? "Stop" : "Restart"} ${sessionLabel(confirm.row)} in ${basename(confirm.row.directory) || "this folder"}?`}
            description={
              confirm.kind === "stop"
                ? `On ${confirm.row.machineName}. Its current conversation may end.`
                : `On ${confirm.row.machineName}. Its current conversation may end unless the agent supports resume.`
            }
            destructiveLabel={confirm.kind === "stop" ? "Stop agent" : "Restart agent"}
            onConfirm={() => {
              const { kind, row } = confirm
              setConfirm(null)
              if (kind === "stop") void commands.stop(row)
              else void commands.restart(row)
            }}
          />
        )}
      </ConfirmAlert>
      <NewSessionSheet open={search.new === "1"} machines={machines} onOpenChange={closeNewSession} />
    </div>
  )
}
