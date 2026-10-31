import type { KeyboardEvent } from "react"
import { Link } from "@tanstack/react-router"
import { Package, Pencil, Plug, RotateCw, Save, Square, X, type LucideIcon } from "lucide-react"
import { SourceRow } from "@/components/kit/list"
import { StatusGlyph } from "@/components/kit/status-dot"
import { Menu, MenuContent, MenuItem, MenuTrigger, RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger, RowMoreButton } from "@/components/kit/overlays"
import { ContextMenuSeparator } from "@/components/ui/context-menu"
import { DropdownMenuSeparator } from "@/components/ui/dropdown-menu"
import { shortAge } from "@/lib/format"
import { cn } from "@/lib/utils"
import { rowKey, sessionDetail, sessionLabel, type ListedSession } from "./group"
import { sessionRules } from "./rules"

export type RowActions = {
  rename: () => void
  restart: () => void
  stop: () => void
  reconnect: () => void
  closeTerminal: () => void
  install: () => void
  save: () => void
}

type Entry = { label: string; icon: LucideIcon; shortcut?: string; disabled: boolean; run: () => void } | "separator"

type RowState = "running" | "stopped" | "offline" | "restarting"

function menuEntries(session: ListedSession, selected: boolean, actions: RowActions): Entry[] {
  const rules = sessionRules(session)
  return [
    { label: "Rename…", icon: Pencil, shortcut: "F2", disabled: !rules.rename, run: actions.rename },
    { label: "Restart…", icon: RotateCw, disabled: !rules.restart, run: actions.restart },
    { label: "Stop…", icon: Square, disabled: !rules.stop, run: actions.stop },
    "separator",
    { label: "Reconnect", icon: Plug, disabled: !rules.reconnect, run: actions.reconnect },
    { label: "Close terminal", icon: X, disabled: !selected, run: actions.closeTerminal },
    "separator",
    { label: "Install package…", icon: Package, disabled: !rules.install, run: actions.install },
    { label: "Save agentpack…", icon: Save, disabled: !rules.save, run: actions.save },
  ]
}

// Context menus and the ⋯ menu share one entry list; only their item components differ.
function renderEntries(entries: Entry[], context: boolean) {
  return entries.map((entry, index) => {
    if (entry === "separator") {
      return context ? <ContextMenuSeparator key={index} /> : <DropdownMenuSeparator key={index} />
    }
    const Icon = entry.icon
    return context ? (
      <RowContextMenuItem key={entry.label} icon={<Icon aria-hidden />} shortcut={entry.shortcut} disabled={entry.disabled} onClick={entry.run}>
        {entry.label}
      </RowContextMenuItem>
    ) : (
      <MenuItem key={entry.label} icon={<Icon aria-hidden />} shortcut={entry.shortcut} disabled={entry.disabled} onClick={entry.run}>
        {entry.label}
      </MenuItem>
    )
  })
}

const dimWord = "shrink-0 text-(length:--fs-sm) text-(--fg3) sel:text-(--accent-fg)"

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
  actions,
}: {
  session: ListedSession
  selected: boolean
  tabStop: boolean
  actions: RowActions
}) {
  const { state } = session
  const rowState: RowState = session.offline ? "offline" : state.restarting ? "restarting" : state.running ? "running" : "stopped"
  const word = session.offline ? "Offline" : state.restarting ? "Restarting" : state.running ? undefined : "Stopped"
  const mode = session.mode && session.mode !== "default" ? session.mode : undefined
  const entries = menuEntries(session, selected, actions)
  const since = session.last_used ?? session.started_at
  const age = since ? shortAge(new Date(since)) : undefined

  return (
    <RowContextMenu>
      <RowContextMenuTrigger render={<div className="group/row relative" data-selected={selected ? "true" : undefined} onKeyDown={openFromKeyboard} />}>
        <SourceRow
          render={
            <Link
              to="/sessions/$machine/$sessionId"
              params={{ machine: session.machine, sessionId: session.id }}
              search={true}
              title={sessionDetail(session)}
            />
          }
          selected={selected}
          tabIndex={tabStop ? 0 : -1}
          data-row-key={rowKey(session)}
          className={cn("pe-(--head-h)", session.offline && "text-(--fg3)")}
        >
          <StatusGlyph state={rowState} className="ms-(--s7)" />
          <span className="min-w-0 flex-1 truncate">{sessionLabel(session)}</span>
          {mode && <span className={dimWord}>{mode}</span>}
          {/* The age is decoration: the row's title has it, and it must not churn the row's accessible name. */}
          <span aria-hidden={!word} className={dimWord}>{word ?? age}</span>
        </SourceRow>
        <Menu>
          <MenuTrigger render={<RowMoreButton className="absolute top-1/2 right-(--s1) -translate-y-1/2" />} />
          <MenuContent align="end">{renderEntries(entries, false)}</MenuContent>
        </Menu>
      </RowContextMenuTrigger>
      <RowContextMenuContent>{renderEntries(entries, true)}</RowContextMenuContent>
    </RowContextMenu>
  )
}
