import { useEffect, useRef, useState, type KeyboardEvent } from "react"
import { Link, useNavigate } from "@tanstack/react-router"
import { Copy, Pencil, RotateCcw, SquareArrowOutUpRight } from "lucide-react"
import { AgentTile } from "@/components/kit/agent-tile"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { FilterToken } from "@/components/kit/filters"
import { GroupHeader, SearchField, SourceList, SourceRow } from "@/components/kit/list"
import { RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { basename, harnessName } from "@/lib/format"
import { copyText } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { useSessionCommands } from "./actions"
import { projectDirectory } from "./git"
import { lastActivity, matchesSession, rowKey, sessionLabel, type ListedSession } from "./group"
import { useListedSessions } from "./queries"
import { sessionRules } from "./rules"
import { sessionGlyphState, sessionStatusWord } from "./session-status"
import { RowSheet, type SheetState } from "./session-sheets"

// URL keys, distinct from the sidebar filter's so each view keeps its own: find (text), project (folder name), on (machine id), by (agent).
export type HistorySearch = { find?: string; project?: string; on?: string; by?: string }
type Facet = "project" | "on" | "by"

const DAY = 86_400_000
const midnight = (time: number) => new Date(new Date(time).toDateString()).getTime()

// Safari- and Messages-style day sections: Today, Yesterday, the weekday this week, then months.
export function historySection(time: number, now: number): string {
  const days = Math.round((midnight(now) - midnight(time)) / DAY)
  const date = new Date(time)
  const today = new Date(now)
  if (days <= 0) return "Today"
  if (days === 1) return "Yesterday"
  if (days < 7) return date.toLocaleDateString("en", { weekday: "long" })
  if (date.getFullYear() === today.getFullYear() && date.getMonth() === today.getMonth()) return "Earlier this month"
  return date.toLocaleDateString("en", date.getFullYear() === today.getFullYear() ? { month: "long" } : { month: "long", year: "numeric" })
}

// Recent sections already name the day, so their rows show the time; older rows show the date.
function rowTime(time: number, now: number): string {
  const date = new Date(time)
  return now - time < 7 * DAY ? date.toLocaleTimeString("en", { hour: "numeric", minute: "2-digit" }) : date.toLocaleDateString("en", { month: "short", day: "numeric" })
}

const projectOf = (session: ListedSession) => basename(projectDirectory(session)) || "Unknown project"

// The Work page's other view: every chat on every machine, newest first, open, closed and cold-stored.
// Searched as you type and narrowed with tokens (HIG Search fields). The Work page owns the scroller,
// so coming back from a session restores the position the way it does for Work.
export function HistoryView({ search, setSearch }: { search: HistorySearch; setSearch: (patch: HistorySearch) => void }) {
  const { machines, queries, sessions } = useListedSessions()
  const commands = useSessionCommands()
  const navigate = useNavigate()
  const isPhone = useIsPhone()
  const [text, setText] = useState(search.find ?? "")
  const [sheet, setSheet] = useState<SheetState | null>(null)
  // SearchField keeps its own ref (it already answers ⌘F), so the input is found through its header.
  const head = useRef<HTMLDivElement>(null)
  const input = () => head.current?.querySelector("input")
  const list = useRef<HTMLDivElement>(null)
  const now = Date.now()

  // ⌘F searches History here, before the sidebar's own ⌘F sees it.
  useEffect(() => {
    function onFind(event: globalThis.KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "f") {
        event.preventDefault()
        event.stopPropagation()
        input()?.focus()
        input()?.select()
      }
    }
    window.addEventListener("keydown", onFind, true)
    return () => window.removeEventListener("keydown", onFind, true)
  }, [])

  // A terminal has no transcript to come back to, so History lists chats.
  const all = sessions.filter((session) => session.transport === "acp" && !session.provisionalStart)
  const machineName = (id: string) => machines.find((machine) => machine.id === id)?.name ?? id
  const filtered = all.filter((session) =>
    (!search.project || projectOf(session) === search.project) &&
    (!search.on || session.machine === search.on) &&
    (!search.by || session.harness === search.by))
  const shown = filtered.filter((session) => matchesSession(session, text) || projectOf(session).toLowerCase().includes(text.trim().toLowerCase()))
    .sort((a, b) => (Date.parse(lastActivity(b) ?? "") || 0) - (Date.parse(lastActivity(a) ?? "") || 0))
  const sections: { label: string; rows: ListedSession[] }[] = []
  for (const row of shown) {
    const label = historySection(Date.parse(lastActivity(row) ?? "") || 0, now)
    if (sections.at(-1)?.label === label) sections.at(-1)!.rows.push(row)
    else sections.push({ label, rows: [row] })
  }

  // Typing "agentpack" offers it as a project token, so people learn the tokens exist (HIG: pair tokens with suggestions).
  const needle = text.trim().toLowerCase()
  const suggestions: { facet: Facet; label: string; value: string; display: string }[] = needle ? [
    ...[...new Set(all.map(projectOf))].filter((name) => name.toLowerCase().includes(needle) && name !== search.project).map((name) => ({ facet: "project" as const, label: "Project", value: name, display: name })),
    ...machines.filter((machine) => machine.name.toLowerCase().includes(needle) && machine.id !== search.on).map((machine) => ({ facet: "on" as const, label: "Machine", value: machine.id, display: machine.name })),
    ...[...new Set(all.map((session) => session.harness))].filter((id) => harnessName(id).toLowerCase().includes(needle) && id !== search.by).map((id) => ({ facet: "by" as const, label: "Agent", value: id, display: harnessName(id) })),
  ].slice(0, 4) : []
  const tokens: { facet: Facet; label: string; value: string }[] = [
    ...(search.project ? [{ facet: "project" as const, label: "Project", value: search.project }] : []),
    ...(search.on ? [{ facet: "on" as const, label: "Machine", value: machineName(search.on) }] : []),
    ...(search.by ? [{ facet: "by" as const, label: "Agent", value: harnessName(search.by) }] : []),
  ]
  const narrowed = tokens.length > 0 || needle !== ""

  function type(value: string) {
    setText(value)
    setSearch({ find: value || undefined })
  }

  function choose(facet: Facet, value: string) {
    setText("")
    setSearch({ find: undefined, [facet]: value })
    input()?.focus()
  }

  function onFieldKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown") {
      event.preventDefault()
      list.current?.querySelector<HTMLElement>("[data-source-row]")?.focus()
    } else if (event.key === "Escape" && text) {
      event.preventDefault()
      type("")
    } else if (event.key === "Backspace" && !text && tokens.length) {
      setSearch({ [tokens.at(-1)!.facet]: undefined })
    }
  }

  const loading = queries.firstLoadPending
  const failed = machines.flatMap((machine) => queries.errors[machine.id] ? [machine.name] : [])

  return (
    <>
      <div ref={head} className="flex flex-col gap-(--s3)">
        <SearchField autoFocus={!isPhone} aria-label="Search history" placeholder="Search history"
          value={text} onChange={(event) => type(event.currentTarget.value)} onKeyDown={onFieldKey} />
        {(tokens.length > 0 || suggestions.length > 0) && (
          <div role="group" aria-label="Search tokens" className="flex flex-wrap items-center gap-(--s2)">
            {tokens.map((token) => (
              <FilterToken key={token.facet} label={token.label} value={token.value} onOpen={() => input()?.focus()} onRemove={() => setSearch({ [token.facet]: undefined })} />
            ))}
            {suggestions.map((suggestion) => (
              <button key={`${suggestion.facet}:${suggestion.value}`} type="button" onClick={() => choose(suggestion.facet, suggestion.value)}
                className="flex h-(--ctl-h-sm) items-center gap-(--s2) rounded-(--r-pill) border-[0.5px] border-dashed border-(--control-edge) px-(--s3) text-(length:--fs-sm) text-(--fg2) hover:bg-(--hover) hover:text-(--fg)">
                <span className="text-(--fg3)">{suggestion.label}</span> {suggestion.display}
              </button>
            ))}
            {tokens.length > 0 && (
              <span className="ms-auto flex items-center gap-(--s2) text-(length:--fs-xs) text-(--fg2) tabular-nums">
                {`${shown.length} of ${all.length}`}
                <button type="button" onClick={() => { setText(""); setSearch({ find: undefined, project: undefined, on: undefined, by: undefined }) }}
                  className="rounded-(--r-xs) font-medium text-(--accent) hover:underline">Clear</button>
              </span>
            )}
          </div>
        )}
      </div>
      <div ref={list} className="-mx-(--s3)">
        <div>
          {failed.length > 0 && <Banner tone="warn">{`Couldn't read history from ${failed.join(", ")}. Showing what this gateway has.`}</Banner>}
          {loading && [0, 1, 2, 3].map((index) => <RowSkeleton key={index} lines={2} />)}
          {!loading && shown.length === 0 && (narrowed ? (
            <Empty title="No matching sessions">
              {needle ? `Nothing in History matches “${text.trim()}”. ` : "Nothing matches these tokens. "}
              <button type="button" onClick={() => { setText(""); setSearch({ find: undefined, project: undefined, on: undefined, by: undefined }) }} className="font-medium text-(--accent) hover:underline">Search everything</button>
            </Empty>
          ) : (
            <Empty title="No sessions yet">Every chat you start lands here, and stays after you close it.</Empty>
          ))}
          <SourceList aria-label="History" className="overflow-visible p-0">
            {sections.map((section) => (
              <section key={section.label} aria-label={section.label} data-work-group={section.label}>
                <GroupHeader className="sticky top-0 z-1 bg-(--page) px-(--s3)"><h2 tabIndex={-1}>{section.label}</h2></GroupHeader>
                {section.rows.map((row) => (
                  <HistoryRow key={rowKey(row)} row={row} time={rowTime(Date.parse(lastActivity(row) ?? "") || 0, now)}
                    onReopen={() => void commands.reopen([row])} onRename={() => setSheet({ kind: "rename", row })}
                    onOpen={() => void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: row.machine, sessionId: row.id } })} />
                ))}
              </section>
            ))}
          </SourceList>
        </div>
      </div>
      <RowSheet sheet={sheet} onClose={() => setSheet(null)} />
    </>
  )
}

function HistoryRow({ row, time, onOpen, onReopen, onRename }: { row: ListedSession; time: string; onOpen: () => void; onReopen: () => void; onRename: () => void }) {
  const rules = sessionRules(row)
  const title = sessionLabel(row)
  const word = sessionStatusWord(row, false)
  const state = sessionGlyphState(row)
  const detail = [projectOf(row), row.machineName, harnessName(row.harness), word].filter(Boolean).join(" · ")
  const reopen = rules.reopen && !row.archived_at
  return (
    <RowContextMenu>
      <RowContextMenuTrigger render={<div className="group/row relative" />}>
        <SourceRow render={<Link to="/sessions/$machine/$sessionId" params={{ machine: row.machine, sessionId: row.id }} />}
          className="h-auto min-h-(--row-h-2) py-(--s2)">
          <AgentTile harness={row.harness} state={state} label={word ?? "Open"} />
          <span className="flex min-w-0 flex-1 flex-col">
            <span className={cn("truncate", row.closed_at || row.archived_at ? "text-(--fg2)" : "font-medium")}>{title}</span>
            <span className="truncate text-(length:--fs-xs) text-(--fg2) sel:text-(--accent-fg)">{detail}</span>
          </span>
          <span className={cn("shrink-0 text-(length:--fs-sm) text-(--fg3) tabular-nums sel:text-(--accent-fg)", reopen && "pointer-fine:group-hover/row:invisible pointer-fine:group-focus-within/row:invisible")}>{time}</span>
        </SourceRow>
        {reopen && (
          // The undo of Close, on the row it applies to. Touch reopens from the long-press menu or the session's banner.
          <Button type="button" size="sm" onClick={onReopen} aria-label={`Reopen ${title}`}
            className="absolute top-1/2 right-(--s3) -translate-y-1/2 opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100 group-focus-within/row:opacity-100 pointer-coarse:hidden">
            Reopen
          </Button>
        )}
      </RowContextMenuTrigger>
      <RowContextMenuContent>
        <RowContextMenuItem icon={<SquareArrowOutUpRight aria-hidden />} onClick={onOpen}>Open</RowContextMenuItem>
        {reopen && <RowContextMenuItem icon={<RotateCcw aria-hidden />} onClick={onReopen}>Reopen</RowContextMenuItem>}
        {rules.rename && <RowContextMenuItem icon={<Pencil aria-hidden />} onClick={onRename}>Rename…</RowContextMenuItem>}
        <RowContextMenuItem icon={<Copy aria-hidden />} onClick={() => void copyText(row.id)}>Copy session ID</RowContextMenuItem>
      </RowContextMenuContent>
    </RowContextMenu>
  )
}

