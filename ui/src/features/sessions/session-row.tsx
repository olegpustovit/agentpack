import type { KeyboardEvent } from "react"
import { Link } from "@tanstack/react-router"
import { Archive, Asterisk, Check, createLucideIcon, Bot, Box, Braces, Copy, GitBranch, MousePointer2, Orbit, Package, Pencil, Pi, Plug, RotateCw, Save, Square, X, Zap, type LucideIcon } from "lucide-react"
import { SourceRow } from "@/components/kit/list"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import type { SubagentSummary } from "@/lib/api-types"
import { clock, goalElapsedMs, goalStateWords, liveGoal } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import { copyText } from "@/lib/hud"
import { Menu, MenuContent, MenuItem, MenuTrigger, RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger, RowMoreButton } from "@/components/kit/overlays"
import { ContextMenuSeparator } from "@/components/ui/context-menu"
import { DropdownMenuSeparator } from "@/components/ui/dropdown-menu"
import { basename, harnessName, shortAge } from "@/lib/format"
import { cn } from "@/lib/utils"
import { lastActivity, rowKey, sessionDetail, sessionLabel, type ListedSession } from "./group"
import { isWorktreePath } from "./git"
import { sessionGlyphState, sessionStatusWord, useSteadyGlyph, type GlyphState } from "./session-status"
import { sessionRules } from "./rules"

export type RowActions = {
  rename: () => void
  restart: () => void
  stop: () => void
  archive: () => void
  reconnect: () => void
  closeTerminal: () => void
  install: () => void
  save: () => void
  tools: () => void
}

// Actions on every marked row (⌘- or Shift-click). Absent actions mean no marked row allows them.
export type GroupActions = { stopCount: number; archiveCount: number; stop: () => void; archive: () => void; ids: string[] }

// `checked` makes the entry a checkbox item: a check when on, an empty icon slot when off.
type Entry = { label: string; icon: LucideIcon; shortcut?: string; disabled: boolean; checked?: boolean; run: () => void } | "separator"

const Unchecked = createLucideIcon("unchecked", [])
const archiveShortcut = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform) ? "⌘⌫" : "Ctrl+Del"

function groupEntries(group: GroupActions): Entry[] {
  return [
    { label: group.stopCount > 1 ? `Stop ${group.stopCount} agents…` : "Stop agent…", icon: Square, disabled: group.stopCount === 0, run: group.stop },
    { label: group.archiveCount > 1 ? `Archive ${group.archiveCount} sessions` : "Archive", icon: Archive, shortcut: archiveShortcut, disabled: group.archiveCount === 0, run: group.archive },
    "separator",
    { label: `Copy ${group.ids.length} session IDs`, icon: Copy, disabled: false, run: () => copySessionId(group.ids.join("\n")) },
  ]
}

function menuEntries(session: ListedSession, selected: boolean, actions: RowActions): Entry[] {
  const rules = sessionRules(session)
  return [
    { label: "Rename…", icon: Pencil, shortcut: "F2", disabled: !rules.rename, run: actions.rename },
    { label: "Restart…", icon: RotateCw, disabled: !rules.restart, run: actions.restart },
    { label: "Archive", icon: Archive, shortcut: archiveShortcut, disabled: !rules.archive, run: actions.archive },
    { label: "Stop agent…", icon: Square, disabled: !rules.stop, run: actions.stop },
    // A chat has no terminal to reconnect or close.
    ...(session.transport === "acp" ? [] : [
      "separator" as const,
      { label: "Reconnect", icon: Plug, disabled: !rules.reconnect, run: actions.reconnect },
      { label: "Close terminal", icon: X, disabled: !selected, run: actions.closeTerminal },
    ]),
    ...(session.transport === "acp" ? [
      "separator" as const,
      { label: "agentpack Tools", icon: session.tools ? Check : Unchecked, checked: Boolean(session.tools), disabled: session.offline || Boolean(session.archived_at), run: actions.tools },
    ] : []),
    "separator",
    { label: "Install package…", icon: Package, disabled: !rules.install, run: actions.install },
    { label: "Save agentpack…", icon: Save, disabled: !rules.save, run: actions.save },
    "separator",
    // For bug reports: the ID finds the session's state and diagnostic traces.
    { label: "Copy session ID", icon: Copy, disabled: false, run: () => copySessionId(session.id) },
  ]
}

function copySessionId(text: string) {
  void copyText(text, `Couldn't copy. ${text.replaceAll("\n", ", ")}`)
}

// Context menus and the ⋯ menu share one entry list; only their item components differ.
function renderEntries(entries: Entry[], context: boolean) {
  return entries.map((entry, index) => {
    if (entry === "separator") {
      return context ? <ContextMenuSeparator key={index} /> : <DropdownMenuSeparator key={index} />
    }
    const Icon = entry.icon
    const check = entry.checked === undefined ? {} : { role: "menuitemcheckbox", "aria-checked": entry.checked }
    return context ? (
      <RowContextMenuItem key={entry.label} icon={<Icon aria-hidden />} shortcut={entry.shortcut} disabled={entry.disabled} onClick={entry.run} {...check}>
        {entry.label}
      </RowContextMenuItem>
    ) : (
      <MenuItem key={entry.label} icon={<Icon aria-hidden />} shortcut={entry.shortcut} disabled={entry.disabled} onClick={entry.run} {...check}>
        {entry.label}
      </MenuItem>
    )
  })
}

const dimWord = "shrink-0 text-(length:--fs-sm) text-(--fg3) tabular-nums sel:text-(--accent-fg)"

const agentIcons: Record<string, LucideIcon> = {
  claude: Asterisk,
  codex: Box,
  opencode: Braces,
  cursor: MousePointer2,
  agent: MousePointer2,
  grok: Zap,
  omp: Pi,
  agy: Orbit,
}

const stateWords: Record<GlyphState, string> = {
  running: "Working",
  needsYou: "Needs you",
  idle: "Idle",
  stopped: "Stopped",
  restarting: "Restarting",
  offline: "Offline",
  starting: "Starting",
}

// The agent's icon with a corner status dot. One element whose data-state changes, so refetches
// never restart its pulse.
export function AgentTile({ harness, state, label, title, className, unread = false }: { harness: string; state: GlyphState | "requires_action"; label: string; title?: string; className?: string; unread?: boolean }) {
  const Icon = agentIcons[harness] ?? Bot
  const dot = state === "needsYou" || state === "requires_action" ? "needs" : state === "running" ? "working" : state === "restarting" ? "restarting" : state === "starting" ? "starting" : unread ? "done" : "none"
  return (
    <span data-slot="agent-tile" role="img" aria-label={label} title={title} className={cn("relative inline-grid size-[18px] shrink-0 place-items-center rounded-[5px] bg-(--track) text-(--fg2) sel:bg-(--accent-fg)/20 sel:text-(--accent-fg)", className)}>
      <Icon aria-hidden strokeWidth={1.75} className="size-3" />
      <span data-slot="agent-dot" data-state={dot} aria-hidden className="agent-dot" />
    </span>
  )
}

// Re-renders once a held state expires, so the row settles without waiting for a refetch.
// Only chats are held: a terminal session's state is its process, which never blips.
// WebKit sends no contextmenu event for Shift+F10 or the context-menu key, so the key opens the menu itself.
function openFromKeyboard(event: KeyboardEvent<HTMLElement>) {
  if (!(event.key === "ContextMenu" || (event.key === "F10" && event.shiftKey))) return
  event.preventDefault()
  const box = event.currentTarget.getBoundingClientRect()
  event.currentTarget.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: box.left + box.width / 2, clientY: box.top + box.height / 2 }))
}

// Roving focus is React-managed (tabStop), so the kit's SourceList is not used: it rewrites
// tabindex on every render, which would churn the DOM on each refetch.
export function SessionRow({
  session,
  selected,
  tabStop,
  stopping,
  unread,
  actions,
  marked = false,
  group,
  nested = false,
}: {
  session: ListedSession
  selected: boolean
  tabStop: boolean
  stopping: boolean
  unread: boolean
  actions: RowActions
  // Part of a multi-row selection; `group` replaces the menu while two or more rows are marked.
  marked?: boolean
  group?: GroupActions
  // A helper chat listed under the chat whose agent started it.
  nested?: boolean
}) {
  const label = sessionLabel(session)
  const chat = session.transport === "acp"
  const state = useSteadyGlyph(sessionGlyphState(session), chat)
  const word = state === "running" || state === "idle" ? stopping ? "Stopping…" : undefined : sessionStatusWord(session, stopping)
  const entries = group ? groupEntries(group) : menuEntries(session, selected, actions)
  const since = lastActivity(session)
  // Ages tick once a minute; a working chat is active now whatever its last refetch said.
  const minute = useNow(true, 60_000)
  const age = state === "starting" ? undefined : state === "running" && chat ? "now" : since ? shortAge(new Date(since), minute) : undefined
  const goal = liveGoal(session.goal) ? session.goal : undefined
  const now = useNow(goal?.state === "active")
  const goalClock = goal && clock(goalElapsedMs(goal, now))
  const mode = session.mode && session.mode !== "default" ? session.mode : undefined
  const stateWord = state === "running" && !chat ? "Running" : stateWords[state]
  const detail = [harnessName(session.harness), mode, basename(session.directory), session.machineName, word ?? stateWord, goal && `${goalStateWords[goal.state]} ${goalClock}`].filter(Boolean).join(" · ")

  return (
    <RowContextMenu>
      <RowContextMenuTrigger render={<div className="group/row relative" data-selected={selected || marked ? "true" : undefined} onKeyDown={openFromKeyboard} />}>
        <Tooltip>
          <SourceRow
            render={
              <TooltipTrigger
                render={
                  <Link
                    to="/sessions/$machine/$sessionId"
                    params={{ machine: session.machine, sessionId: session.id }}
                    search={(prev: Record<string, unknown>) => ({ ...prev, sub: undefined })}
                  />
                }
              />
            }
            selected={selected || marked}
            aria-current={selected ? "true" : undefined}
            data-marked={marked || undefined}
            tabIndex={tabStop ? 0 : -1}
            data-row-key={rowKey(session)}
            className={cn("pe-(--head-h)", nested && "ps-(--s7)", session.offline && "text-(--fg3)")}
          >
            <AgentTile className="ms-(--s6)" harness={session.harness} state={state} unread={unread} label={unread ? "Finished, unread" : stateWord} title={`${harnessName(session.harness)}, ${unread ? "Finished, unread" : stateWord}`} />
            {/* Not hit-testable: Safari adds its own tooltip to ellipsized text under the pointer, doubling the row's. */}
            <span className={cn("pointer-events-none min-w-0 flex-1 truncate", unread && "font-semibold")}>{label}</span>
            {isWorktreePath(session.directory) && <GitBranch aria-label="In its own worktree" className="size-3 shrink-0 opacity-60" />}
            {/* One trailing item. The age is decoration: the tooltip has it. */}
            {word ? (
              <span className={cn(dimWord, state === "needsYou" && "font-medium text-(--warn-word)")}>{word}</span>
            ) : goal ? (
              <span className={dimWord} aria-label={goalStateWords[goal.state]}>◎ {goalClock}</span>
            ) : (
              <span aria-hidden className={dimWord}>{age}</span>
            )}
          </SourceRow>
          <TooltipContent side="right" className="flex-col items-start gap-0">
            <strong className="font-semibold">{label}</strong>
            <span>{detail}</span>
            <span>{sessionDetail(session).split(" · ").slice(3).join(" · ")}</span>
          </TooltipContent>
        </Tooltip>
        <Menu>
          <MenuTrigger render={<RowMoreButton className="absolute top-1/2 right-(--s1) -translate-y-1/2" />} />
          <MenuContent align="end">{renderEntries(entries, false)}</MenuContent>
        </Menu>
      </RowContextMenuTrigger>
      <RowContextMenuContent>{renderEntries(entries, true)}</RowContextMenuContent>
    </RowContextMenu>
  )
}

// A running subagent, indented under its chat. Opens it in the main view.
export function SubagentRow({ session, subagent, selected }: { session: ListedSession; subagent: SubagentSummary; selected: boolean }) {
  const now = useNow(true)
  const elapsed = subagent.started_at ? clock(now - subagent.started_at) : undefined
  return (
    <SourceRow
      render={
        <Link
          to="/sessions/$machine/$sessionId"
          params={{ machine: session.machine, sessionId: session.id }}
          search={(prev: Record<string, unknown>) => ({ ...prev, sub: subagent.id })}
        />
      }
      selected={selected}
      tabIndex={-1}
      data-subagent-row={subagent.id}
      className="ps-(--s7) text-(length:--fs-sm)"
    >
      <AgentTile className="ms-(--s6)" harness={session.harness} state={subagent.state === "requires_action" ? "requires_action" : "running"} label={subagent.state === "requires_action" ? "Needs you" : "Running"} />
      <span className="min-w-0 flex-1 truncate">{subagent.title || "Subagent"}</span>
      {elapsed && <span aria-hidden className={dimWord}>{elapsed}</span>}
    </SourceRow>
  )
}
