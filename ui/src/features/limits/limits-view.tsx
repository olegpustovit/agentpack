import { useEffect, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Meter } from "@/components/kit/data"
import { Empty } from "@/components/kit/feedback"
import { AgentTile } from "@/components/kit/agent-tile"
import type { Machine } from "@/features/machines/queries"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { api, LOCAL } from "@/lib/api"
import { harnessName, relativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"
import { limitsQuery } from "./queries"
import { absoluteResetTime, agentLimits, isWindowStale, paceText, resetText, useLimitGroups, type AgentLimit } from "./model"

// Per tab, when each peer was last asked: views showing limits share it, so moving between them asks nobody twice.
const nudged = new Map<string, number>()
const NUDGE_MS = 5 * 60_000

// Peers write their limits only after their own turns, or when asked. While limits are on screen, ask every
// online peer to read its CLIs again; a failed nudge leaves the replicated reading, which is dated.
export function useLimitsNudge(machines: Machine[]) {
  const queryClient = useQueryClient()
  const peers = machines.filter((machine) => machine.online && machine.id !== LOCAL).map((machine) => machine.id).join("\n")
  useEffect(() => {
    const now = Date.now()
    for (const id of peers.split("\n").filter(Boolean)) {
      if (now - (nudged.get(id) ?? -Infinity) < NUDGE_MS) continue
      nudged.set(id, now)
      api("/api/limits/refresh", { machine: id, method: "POST" }).then(
        () => queryClient.invalidateQueries({ queryKey: limitsQuery(false).queryKey }),
        () => undefined,
      )
    }
  }, [peers, queryClient])
}

// One row per agent: what is left of its tightest window, from the newest reading on any machine.
export function LimitsView() {
  const { machines, groups, queries } = useLimitGroups()
  useLimitsNudge(machines)
  const now = useMinuteTick()
  const rows = agentLimits(groups, now)
  const latest = Math.max(0, ...rows.map((row) => row.observed))

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ToolbarTitle title="Limits" subtitle={latest > 0 ? `Updated ${relativeTime(new Date(latest), Math.max(now, latest))}` : undefined} />
      {rows.length === 0 ? (
        <Empty title={queries.firstLoadPending ? "Reading limits…" : "No limits yet"}>Limits appear once an agent is signed in on any machine.</Empty>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <ul aria-label="Limits" className="mx-auto flex w-full max-w-170 flex-col divide-y-[0.5px] divide-(--sep-soft) px-(--s6) py-(--s6) max-[760px]:px-(--s5)">
            {rows.map((row) => <LimitRow key={row.provider} row={row} now={now} />)}
          </ul>
        </div>
      )}
    </div>
  )
}

function LimitRow({ row, now }: { row: AgentLimit; now: number }) {
  const name = harnessName(row.provider)
  const { window } = row
  let note = ""
  let warn = false
  if (window && row.left !== undefined) {
    const reset = resetText(window, now).text
    if (isWindowStale(window, row.observed, now)) note = row.observed > 0 ? `As of ${relativeTime(new Date(row.observed), Math.max(now, row.observed))}` : reset
    else {
      const pace = paceText(window, now)
      warn = pace.warn
      note = warn ? `${pace.text} · ${reset}` : reset
    }
  }
  return (
    <li data-provider={row.provider} className="flex min-h-(--row-h-2) items-center gap-(--s4) py-(--s3)">
      <AgentTile harness={row.provider} state="idle" label={name} />
      <div className="min-w-0 flex-1">
        <p className="font-medium">{name}</p>
        {note && <p className={cn("truncate text-(length:--fs-sm) text-(--fg2) first-letter:uppercase", warn && "text-(--warn-word)")} title={window ? [window.label, absoluteResetTime(window, now)].filter(Boolean).join(" · ") : undefined}>{note}</p>}
      </div>
      {row.left === undefined ? (
        <Link to="/settings/$pane" params={{ pane: "connections" }} className="text-(length:--fs-sm) text-(--accent) hover:underline">Sign in</Link>
      ) : (
        <>
          <span className="w-40 shrink-0 max-[760px]:w-20"><Meter value={row.left} label={`${name} left`} /></span>
          <span className="w-12 shrink-0 text-right text-(length:--fs-lg) font-semibold tabular-nums">{`${Math.round(row.left)}%`}</span>
        </>
      )}
    </li>
  )
}

// One timer for the whole view: reset text is minute-granular, so per-row timers would only repeat work.
function useMinuteTick(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  return now
}
