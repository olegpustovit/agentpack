import { useState, type ReactNode } from "react"
import { Autocomplete } from "@base-ui/react/autocomplete"
import { useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { GitFork, GitPullRequest, Plus, Search, Settings, Square, Minimize2 } from "lucide-react"
import { Kbd } from "@/components/kit/feedback"
import { menuRow, menuSurface } from "@/components/kit/overlays"
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog"
import type { ConversationPage } from "@/features/conversation/queries"
import { advertises, agentCommands, cachedCommands } from "@/features/conversation/composer-hints"
import { useSessionCommands } from "@/features/sessions/actions"
import { rowKey, sessionLabel, type ListedSession } from "@/features/sessions/group"
import { isWorktreePath } from "@/features/sessions/git"
import { sessionsQuery } from "@/features/sessions/queries"
import { sessionRules } from "@/features/sessions/rules"
import { sessionGlyphState } from "@/features/sessions/session-status"
import { api, routeMissing } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { basename, harnessName } from "@/lib/format"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { viewLink, views } from "./views"

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)
const mod = isMac ? "⌘" : "Ctrl+"

export type PaletteEntry = {
  id: string
  group: string
  label: string
  // Matched as well as the label, but ranked below it.
  detail?: string
  // Shown dim after the label.
  note?: string
  shortcut?: string
  icon?: ReactNode
  // Listed only once something is typed, so the empty palette stays short.
  typedOnly?: boolean
  run: () => void
}
export type PaletteGroup = { value: string; items: PaletteEntry[] }

// Every word must appear in the label or detail. The label beginning with the query ranks first, then a
// word of the label beginning with it, then the label containing it, then a detail-only match.
function score(entry: PaletteEntry, query: string): number {
  const label = entry.label.toLowerCase()
  const text = `${label} ${entry.detail?.toLowerCase() ?? ""}`
  if (!query.split(/\s+/).every((word) => text.includes(word))) return 0
  if (label.startsWith(query)) return 4
  if (label.split(/[\s/·._-]+/).some((word) => word.startsWith(query))) return 3
  return label.includes(query) ? 2 : 1
}

// Groups keep the order they first appear in; entries within a group sort by score, ties in given order.
export function rankEntries(entries: PaletteEntry[], rawQuery: string): PaletteGroup[] {
  const query = rawQuery.trim().toLowerCase()
  const groups: PaletteGroup[] = []
  const scored = entries
    .map((entry, index) => ({ entry, index, score: query ? score(entry, query) : entry.typedOnly ? 0 : 1 }))
    .filter((item) => item.score > 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
  for (const name of new Set(entries.map((entry) => entry.group))) {
    const items = scored.filter((item) => item.entry.group === name).map((item) => item.entry)
    if (items.length > 0) groups.push({ value: name, items })
  }
  return groups
}

const shortcuts: [string, string][] = [
  ["Command palette", isMac ? "⌘K" : "Ctrl+K (Ctrl+Shift+K in a terminal)"],
  ["Next session that needs you", `${mod}J`],
  ["Sessions, Library, Machines, Usage", `${mod}1–4`],
  ["Toggle the sidebar", `${mod}\\`],
  ["Filter the list", `${mod}F`],
  ["Rename a session", "F2"],
  ["Answer a permission card", "1–9, ↵ for the first"],
  ["Send a message, start a session", `${mod}↵`],
  ["List shortcuts", "?"],
]

// The ⌘K palette: sessions that need you, commands for the open session, then places to go. "?" lists shortcuts.
export default function CommandPalette({
  initialQuery,
  sessions,
  openKey,
  onClose,
}: {
  initialQuery: string
  sessions: ListedSession[]
  openKey: string | undefined
  onClose: () => void
}) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const commands = useSessionCommands()
  const [query, setQuery] = useState(initialQuery)
  const open = sessions.find((session) => rowKey(session) === openKey)

  function go(session: ListedSession) {
    void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: session.machine, sessionId: session.id } })
  }
  function sessionEntry(session: ListedSession, group: string, typedOnly = false): PaletteEntry {
    return {
      id: `session:${rowKey(session)}`,
      group,
      label: sessionLabel(session),
      detail: [harnessName(session.harness), session.mode, basename(session.directory), session.directory, session.machineName].join(" · "),
      note: `${basename(session.directory)} · ${session.machineName}`,
      typedOnly,
      run: () => go(session),
    }
  }

  const waiting = sessions.filter((session) => sessionGlyphState(session) === "needsYou")
  const entries: PaletteEntry[] = waiting.map((session) => sessionEntry(session, "Needs you"))
  if (open) entries.push(...sessionCommands(open))
  entries.push(
    { id: "new", group: "Go to", label: "New session…", icon: <Plus aria-hidden />, run: () => void navigate({ to: "/sessions", search: { new: "1" } }) },
    ...views.map((view, index) => ({ id: `view:${view.id}`, group: "Go to", label: view.label, icon: <view.icon aria-hidden />, shortcut: `${mod}${index + 1}`, run: () => void navigate(viewLink(view)) })),
    { id: "settings", group: "Go to", label: "Settings", icon: <Settings aria-hidden />, run: () => void navigate({ to: "/settings" }) },
    ...sessions.filter((session) => !waiting.includes(session) && !session.archived_at).map((session) => sessionEntry(session, "Sessions", true)),
  )

  function sessionCommands(session: ListedSession): PaletteEntry[] {
    const agent = harnessName(session.harness)
    const live = !session.offline && session.state.running
    const chat = session.transport === "acp"
    const list: PaletteEntry[] = []
    if (sessionRules(session).stop) {
      list.push({ id: "stop", group: "This session", label: `Stop ${agent}`, icon: <Square aria-hidden />, run: () => void commands.stop(session) })
    }
    if (live && chat && session.agent_session_id && session.agent_capabilities?.sessionCapabilities?.fork) {
      list.push({
        id: "fork",
        group: "This session",
        label: "Fork conversation",
        icon: <GitFork aria-hidden />,
        run: () =>
          void api<SessionInfo>(`/api/sessions/${encodeURIComponent(session.id)}/fork`, { machine: session.machine, method: "POST" }).then(
            (next) => {
              void queryClient.invalidateQueries({ queryKey: sessionsQuery(session.machine, false).queryKey })
              void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: session.machine, sessionId: next.id } })
            },
            (error: unknown) => hud.error(`Couldn't fork ${sessionLabel(session)}: ${error instanceof Error ? error.message : String(error)}`),
          ),
      })
    }
    // Offered for a session in its own worktree, as the toolbar does; the gateway pushes then opens the PR.
    if (!session.offline && isWorktreePath(session.directory) && !session.archived_at) {
      list.push({
        id: "pull-request",
        group: "This session",
        label: "Create pull request",
        icon: <GitPullRequest aria-hidden />,
        run: () =>
          void api<{ url?: string; created: boolean }>(`/api/sessions/${encodeURIComponent(session.id)}/git/pr`, { machine: session.machine, method: "POST", body: {} }).then(
            (result) => {
              void queryClient.invalidateQueries({ queryKey: ["m", session.machine, "session-git", session.id] })
              if (result.url) window.open(result.url, "_blank", "noopener,noreferrer")
              else hud.success("Branch pushed")
            },
            (error: unknown) => hud.error(`Couldn't create a pull request for ${sessionLabel(session)}: ${routeMissing(error) ? "Needs update" : error instanceof Error ? error.message : String(error)}`),
          ),
      })
    }
    // The same test the open chat applies: the agent advertised /compact, in this chat or last time.
    const page = queryClient.getQueryData<ConversationPage>(["conversation", session.machine, session.id])
    if (live && chat && advertises(agentCommands(page?.entries ?? []) ?? cachedCommands(session.harness), "compact")) {
      list.push({
        id: "compact",
        group: "This session",
        label: "Compact context",
        icon: <Minimize2 aria-hidden />,
        run: () =>
          void api(`/api/sessions/${encodeURIComponent(session.id)}/conversation/prompt`, { machine: session.machine, method: "POST", body: { text: "/compact" } }).then(
            () => queryClient.invalidateQueries({ queryKey: ["conversation", session.machine, session.id] }),
            (error: unknown) => hud.error(`Couldn't compact ${sessionLabel(session)}: ${error instanceof Error ? error.message : String(error)}`),
          ),
      })
    }
    return list
  }

  const listingShortcuts = query.trim().startsWith("?")
  const groups = listingShortcuts ? [] : rankEntries(entries, query)

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent
        showCloseButton={false}
        aria-label="Command palette"
        className={cn(menuSurface, "top-[12vh] w-[min(600px,calc(100vw-2rem))] max-w-none translate-y-0 gap-0 p-0 sm:max-w-none")}
      >
        <DialogTitle className="sr-only">Command palette</DialogTitle>
        <Autocomplete.Root
          inline
          open
          items={groups}
          filteredItems={groups}
          value={query}
          onValueChange={setQuery}
          autoHighlight="always"
          itemToStringValue={(entry: PaletteEntry) => entry.label}
        >
          <label className="flex h-10 items-center gap-(--s3) border-b-[0.5px] border-(--sep) px-(--s5)">
            <Search aria-hidden className="size-4 shrink-0 text-(--fg3)" />
            <Autocomplete.Input
              aria-label="Search sessions and commands"
              placeholder="Search sessions, projects and commands"
              className="min-w-0 flex-1 bg-transparent text-(length:--fs) text-(--fg) outline-none placeholder:text-(--fg3)"
            />
            <Kbd>esc</Kbd>
          </label>
          {listingShortcuts ? (
            <dl aria-label="Keyboard shortcuts" className="grid grid-cols-[1fr_auto] gap-x-(--s6) gap-y-(--s2) px-(--s5) py-(--s4) text-(length:--fs)">
              {shortcuts.map(([label, keys]) => (
                <div key={label} className="contents">
                  <dt>{label}</dt>
                  <dd className="text-end"><Kbd>{keys}</Kbd></dd>
                </div>
              ))}
            </dl>
          ) : (
            <Autocomplete.List className="max-h-[min(60vh,420px)] overflow-y-auto p-[5px]">
              {(group: PaletteGroup) => (
                <Autocomplete.Group key={group.value} items={group.items} className="pb-(--s2)">
                  <Autocomplete.GroupLabel className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg2)">{group.value}</Autocomplete.GroupLabel>
                  <Autocomplete.Collection>
                    {(entry: PaletteEntry) => (
                      <Autocomplete.Item
                        key={entry.id}
                        value={entry}
                        onClick={() => {
                          onClose()
                          entry.run()
                        }}
                        className={cn(menuRow, "flex cursor-default items-center outline-none data-highlighted:bg-(--accent) data-highlighted:text-(--accent-fg) data-highlighted:**:text-(--accent-fg)")}
                      >
                        {entry.icon}
                        <span className="min-w-0 flex-1 truncate">
                          {entry.label}
                          {entry.note && <span className="text-(--fg3)"> · {entry.note}</span>}
                        </span>
                        {entry.shortcut && <Kbd>{entry.shortcut}</Kbd>}
                      </Autocomplete.Item>
                    )}
                  </Autocomplete.Collection>
                </Autocomplete.Group>
              )}
            </Autocomplete.List>
          )}
          <Autocomplete.Empty className="px-(--s5) py-(--s4) empty:hidden text-(length:--fs-sm) text-(--fg2)">{listingShortcuts ? null : "No matches"}</Autocomplete.Empty>
          <p className="flex gap-(--s5) border-t-[0.5px] border-(--sep) px-(--s5) py-(--s2) text-(length:--fs-xs) text-(--fg3)">
            <span>↑↓ move</span>
            <span>↵ open</span>
            <span className="flex-1" />
            <span>? all shortcuts</span>
          </p>
        </Autocomplete.Root>
      </DialogContent>
    </Dialog>
  )
}
