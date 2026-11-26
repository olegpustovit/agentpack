import { useState, type ReactNode } from "react"
import { Autocomplete } from "@base-ui/react/autocomplete"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Archive, ChartColumn, Gauge, GitFork, GitPullRequest, Plus, Search, Server, Settings, Square, Minimize2 } from "lucide-react"
import { Kbd } from "@/components/kit/feedback"
import { ConfirmAlert, menuRow, menuSurface } from "@/components/kit/overlays"
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog"
import { agentSnapshotQuery, type ConversationPage } from "@/features/conversation/queries"
import { advertises, agentCommands } from "@/features/conversation/composer-hints"
import { PullRequestConfirmation, usePullRequest } from "@/features/conversation/session-git"
import { useSessionCommands } from "@/features/sessions/actions"
import { rowKey, sessionLabel, type ListedSession } from "@/features/sessions/group"
import { isWorktreePath, sessionGitQuery } from "@/features/sessions/git"
import { SessionActionAlert } from "@/features/sessions/session-action-alert"
import { useStartSession } from "@/features/sessions/start"
import { sessionRules } from "@/features/sessions/rules"
import { sessionGlyphState } from "@/features/sessions/session-status"
import { api, errorText, LOCAL } from "@/lib/api"
import { basename, harnessName } from "@/lib/format"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { requestTerminal } from "@/features/terminal/shortcuts"
import { views } from "./views"

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
  review?: boolean
  run: () => void
}
export type PaletteGroup = { value: string; items: PaletteEntry[] }
type Review = { kind: "archive" | "stop"; session: ListedSession } | { kind: "pull-request" }

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
  ["Toggle terminal", `${mod}J / Ctrl+\``],
  ["New shell", "Ctrl+Shift+`"],
  ["Next session that needs you", isMac ? "⌘⇧J" : "Ctrl+Shift+J"],
  ["Sessions, Library, System", `${mod}1–3`],
  ["Toggle the sidebar", `${mod}\\`],
  ["Filter the list", `${mod}F`],
  ["Rename a session", "F2"],
  ["Answer a permission card", "Tab, then Enter on your choice"],
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
  const startSession = useStartSession()
  const [query, setQuery] = useState(initialQuery)
  const [review, setReview] = useState<Review>()
  const open = sessions.find((session) => rowKey(session) === openKey)
  const machine = open?.machine ?? LOCAL
  const sessionId = open?.id ?? ""
  const checkGit = Boolean(open && !open.offline && !open.archived_at && isWorktreePath(open.directory))
  const summary = useQuery({ ...sessionGitQuery(machine, sessionId), enabled: checkGit }).data
  const details = useQuery({ ...sessionGitQuery(machine, sessionId, true), enabled: checkGit && !!summary?.deferred }).data
  const git = summary?.deferred ? details : summary
  const pr = usePullRequest(machine, sessionId, git?.branch)
  const openPullRequest = git?.pr?.state === "OPEN" ? git.pr.url : pr.link?.url

  function archiveSession(session: ListedSession) {
    commands.archive(session)
    if (rowKey(session) === openKey) void navigate({ to: "/sessions", search: true, replace: true })
  }

  function confirmSessionAction({ kind, session }: Extract<Review, { session: ListedSession }>) {
    const current = sessions.find((item) => rowKey(item) === rowKey(session))
    onClose()
    if (!current || !sessionRules(current)[kind]) return
    if (kind === "archive") archiveSession(current)
    else void commands.stop(current)
  }

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
    ...views.map((view, index) => ({ id: `view:${view.id}`, group: "Go to", label: view.label, icon: <view.icon aria-hidden />, shortcut: `${mod}${index + 1}`, run: () => void navigate({ to: view.to }) })),
    { id: "usage", group: "Go to", label: "Usage", icon: <ChartColumn aria-hidden />, run: () => void navigate({ to: "/usage" }) },
    { id: "limits", group: "Go to", label: "Limits", icon: <Gauge aria-hidden />, run: () => void navigate({ to: "/usage/limits" }) },
    { id: "machines", group: "Go to", label: "Machines", icon: <Server aria-hidden />, run: () => void navigate({ to: "/machines" }) },
    { id: "settings", group: "Go to", label: "Settings", icon: <Settings aria-hidden />, run: () => void navigate({ to: "/settings" }) },
    ...sessions.filter((session) => !waiting.includes(session) && !session.archived_at).map((session) => sessionEntry(session, "Sessions", true)),
  )

  function sessionCommands(session: ListedSession): PaletteEntry[] {
    const agent = harnessName(session.harness)
    const live = !session.offline && session.state.running
    const chat = session.transport === "acp"
    const rules = sessionRules(session)
    const list: PaletteEntry[] = []
    if (chat && !session.archived_at) {
      list.push({ id: "terminal", group: "This session", label: "Toggle terminal", shortcut: `${mod}J`, run: () => requestTerminal("toggle") })
      if (!session.offline) list.push({ id: "terminal-new", group: "This session", label: "New shell", shortcut: "Ctrl+Shift+`", run: () => requestTerminal("new") })
    }
    if (rules.stop) {
      list.push({ id: "stop", group: "This session", label: `Stop ${agent}…`, icon: <Square aria-hidden />, review: true, run: () => setReview({ kind: "stop", session }) })
    }
    if (rules.archive) {
      list.push({
        id: "archive",
        group: "This session",
        label: rules.busy ? "Archive session…" : "Archive session",
        icon: <Archive aria-hidden />,
        review: rules.busy,
        run: () => rules.busy ? setReview({ kind: "archive", session }) : archiveSession(session),
      })
    }
    if (live && chat && session.agent_session_id && session.agent_capabilities?.sessionCapabilities?.fork) {
      list.push({
        id: "fork",
        group: "This session",
        label: "Fork conversation",
        icon: <GitFork aria-hidden />,
        run: () => startSession.mutate({
          machine: session.machine, directory: session.directory, harness: session.harness,
          mode: session.mode, view: "chat", forkFrom: session.id,
        }),
      })
    }
    if (openPullRequest) {
      list.push({
        id: "open-pull-request",
        group: "This session",
        label: pr.link && !pr.link.created ? "Open GitHub compare" : "Open pull request",
        icon: <GitPullRequest aria-hidden />,
        run: () => window.open(openPullRequest, "_blank", "noopener,noreferrer"),
      })
    } else if (checkGit && git?.available && git.worktree && !git.removed && git.branch && git.ahead > 0 && !pr.isPending) {
      list.push({
        id: "pull-request",
        group: "This session",
        label: "Create pull request…",
        icon: <GitPullRequest aria-hidden />,
        review: true,
        run: () => setReview({ kind: "pull-request" }),
      })
    }
    // The same test the open chat applies: the agent advertised /compact, in this chat or last time.
    const page = queryClient.getQueryData<ConversationPage>(["conversation", session.machine, session.id])
    const snapshot = queryClient.getQueryData(agentSnapshotQuery(session.machine, session).queryKey)
    if (live && chat && advertises(agentCommands(page?.entries ?? []) ?? snapshot?.commands?.availableCommands ?? [], "compact")) {
      list.push({
        id: "compact",
        group: "This session",
        label: "Compact context",
        icon: <Minimize2 aria-hidden />,
        run: () =>
          void api(`/api/sessions/${encodeURIComponent(session.id)}/conversation/prompt`, { machine: session.machine, method: "POST", body: { text: "/compact" } }).then(
            () => queryClient.invalidateQueries({ queryKey: ["conversation", session.machine, session.id] }),
            (error: unknown) => hud.error(`Couldn't compact ${sessionLabel(session)}: ${errorText(error)}`),
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
                          if (!entry.review) onClose()
                          entry.run()
                        }}
                        className={cn(menuRow, "flex cursor-default items-center outline-none data-highlighted:bg-(--accent-fill) data-highlighted:text-(--accent-fg) data-highlighted:**:text-(--accent-fg)")}
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
          <p className="flex gap-(--s5) border-t-[0.5px] border-(--sep) px-(--s5) py-(--s2) text-(length:--fs-xs) text-(--fg3) [@media(hover:none)]:hidden">
            <span>↑↓ move</span>
            <span>↵ open</span>
            <span className="flex-1" />
            <span>? all shortcuts</span>
          </p>
        </Autocomplete.Root>
        {review && review.kind !== "pull-request" && (
          <SessionActionAlert kind={review.kind} rows={[review.session]} onClose={() => setReview(undefined)} onConfirm={() => confirmSessionAction(review)} />
        )}
        <ConfirmAlert open={review?.kind === "pull-request" && Boolean(git?.branch)} onOpenChange={(next) => { if (!next) setReview(undefined) }}>
          {review?.kind === "pull-request" && git?.branch && <PullRequestConfirmation branch={git.branch} onConfirm={() => { onClose(); pr.mutate() }} />}
        </ConfirmAlert>
      </DialogContent>
    </Dialog>
  )
}
