import { useState } from "react"
import { useQueries } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Banner, RowSkeleton } from "@/components/kit/feedback"
import { SourceRow } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useEventsConnected } from "@/features/events/use-events"
import { harnessName, relativeTime } from "@/lib/format"
import { projectDirectory } from "./git"
import { lastActivity, matchesSession, rowKey, sessionLabel, type ListedSession } from "./group"
import { projectHistoryQuery } from "./queries"
import { placeKey, sessionRules, type ProjectPlace } from "./rules"
import { sessionStatusWord } from "./session-status"
import { useSessionCommands } from "./actions"

export function ProjectHistory({ places, agent, onRename }: {
  places: ProjectPlace[]
  agent: string
  onRename: (row: ListedSession) => void
}) {
  const [filter, setFilter] = useState("")
  const commands = useSessionCommands()
  const live = useEventsConnected()
  const queries = useQueries({ queries: places.map(({ machine, row }) => projectHistoryQuery(machine, row.directory, live)) })
  const matches = new Map<string, ListedSession>()
  for (const entry of places.entries()) {
    const [index, { key, machine, machineName, online }] = entry
    for (const item of queries[index]?.data ?? []) {
      if (key !== placeKey(machine, item.directory) && key !== placeKey(machine, projectDirectory(item))) continue
      const row = { ...item, machine, machineName, offline: !online || item.reachable === false }
      if ((agent === "all" || agent === row.harness) && matchesSession(row, filter)) matches.set(rowKey(row), row)
    }
  }
  const rows = [...matches.values()].sort((a, b) => (Date.parse(lastActivity(b) ?? "") || 0) - (Date.parse(lastActivity(a) ?? "") || 0))
  const loading = queries.some((query) => query.isPending)
  const failed = queries.flatMap((query, index) => query.isError ? [places[index]!.machineName] : [])
  const now = Date.now()

  return (
    <section aria-label="Session history" className="flex min-w-0 flex-col gap-(--s4)">
      <Input type="search" aria-label="Search session history" placeholder="Search session history" value={filter} onChange={(event) => setFilter(event.currentTarget.value)} />
      {failed.length > 0 && (
        <Banner tone="warn" action={<Button type="button" size="sm" onClick={() => {
          for (const query of queries) if (query.isError) void query.refetch()
        }}>Retry</Button>}>
          Couldn’t load history from {[...new Set(failed)].join(", ")}.
        </Banner>
      )}
      {rows.length > 0 && (
        <ul aria-label="Saved sessions" className="m-0 list-none divide-y-[0.5px] divide-(--sep) p-0">
          {rows.map((row) => {
            const title = sessionLabel(row)
            const activity = lastActivity(row)
            const when = activity ? relativeTime(new Date(activity), now) : undefined
            const detail = [harnessName(row.harness), row.machineName, when, sessionStatusWord(row, false)].filter(Boolean).join(" · ")
            return (
              <li key={rowKey(row)} className="flex min-w-0 items-center gap-(--s3) py-(--s2)">
                <SourceRow
                  className="h-auto min-h-(--row-h-2) flex-1 flex-col items-start justify-center gap-0 py-(--s2)"
                  render={<Link to="/sessions/$machine/$sessionId" params={{ machine: row.machine, sessionId: row.id }} search={(prev) => ({
                    q: typeof prev.q === "string" ? prev.q : undefined,
                    group: typeof prev.group === "string" ? prev.group : undefined,
                  })} />}
                >
                  <span className="w-full truncate font-semibold max-[760px]:line-clamp-2 max-[760px]:whitespace-normal" title={title}>{title}</span>
                  <span className="w-full truncate text-(length:--fs-xs) text-(--fg2) max-[760px]:line-clamp-2 max-[760px]:whitespace-normal" title={detail}>{detail}</span>
                </SourceRow>
                {sessionRules(row).reopen && !row.archived_at && (
                  <Button type="button" size="sm" className="max-[760px]:min-h-11" aria-label={`Reopen ${title}`} onClick={() => void commands.reopen([row])}>
                    Reopen
                  </Button>
                )}
                {sessionRules(row).rename && (
                  <Button type="button" variant="ghost" size="sm" className="max-[760px]:min-h-11" aria-label={`Rename ${title}`} onClick={() => onRename(row)}>
                    Rename
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {loading && <div role="status" aria-label="Loading session history"><RowSkeleton lines={2} /><RowSkeleton lines={2} /></div>}
      {!loading && !failed.length && !rows.length && (
        <p className="text-(length:--fs-sm) text-(--fg2)">
          {filter.trim() || agent !== "all" ? "No sessions match these filters." : "No saved sessions for this project yet."}
        </p>
      )}
    </section>
  )
}
