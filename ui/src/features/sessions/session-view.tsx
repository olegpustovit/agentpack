import type { RefObject } from "react"
import { FilterChip, FilterFacet } from "@/components/kit/filters"
import { RefreshCw, RotateCcw } from "lucide-react"
import { PopupSelect } from "@/components/kit/forms"
import { SearchField, SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent } from "@/components/ui/popover"
import { harnessName } from "@/lib/format"
import { cn } from "@/lib/utils"
import { countBy, matchesFilter, statusLabels, statusOf, type SessionFilter, type SessionStatus } from "./filter"
import type { GroupMode, ListedSession } from "./group"

const toggled = <T,>(values: T[], value: T) => values.includes(value) ? values.filter((item) => item !== value) : [...values, value]

// Linear-style view options for the session list. Loaded on first open, so Popover stays out of the route bundle.
export default function SessionView({
  anchor, open, onOpenChange, filter, group, sessions, machines, onFilter, onGroup, onReset, refresh, isDefault,
}: {
  anchor: RefObject<HTMLButtonElement | null>
  open: boolean
  onOpenChange: (open: boolean) => void
  filter: SessionFilter
  group: GroupMode
  sessions: ListedSession[]
  machines: { id: string; name: string }[]
  onFilter: (patch: Partial<SessionFilter>) => void
  onGroup: (group: GroupMode) => void
  onReset: () => void
  refresh: { pending: boolean; run: () => void }
  isDefault: boolean
}) {
  // Faceted counts: each option counts what it would show alongside the other active filters.
  const byStatus = countBy(sessions.filter((session) => matchesFilter(session, { ...filter, statuses: [] })), statusOf)
  const byAgent = countBy(sessions.filter((session) => matchesFilter(session, { ...filter, agents: [] })), (session) => session.harness)
  const allAgents = new Set(sessions.map((session) => session.harness))
  const shownStatuses = (Object.keys(statusLabels) as SessionStatus[]).filter((status) => status !== "offline" || sessions.some((session) => statusOf(session) === "offline") || filter.statuses.includes(status))
  const agents = [...new Set([...allAgents, ...filter.agents])].sort((a, b) => harnessName(a).localeCompare(harnessName(b)))

  return (
    <Popover open={open} onOpenChange={(next, details) => {
      // The lazy popup uses an external anchor. Let the button handle its own presses.
      if (details.reason === "outside-press" && details.event.target instanceof Node && anchor.current?.contains(details.event.target)) {
        details.cancel()
        return
      }
      onOpenChange(next)
    }}>
      <PopoverContent anchor={anchor} align="start" sideOffset={6} aria-label="View options"
        // Focus the search field, except on touch, where it would raise the keyboard over the filters.
        initialFocus={(type) => type !== "touch" && !matchMedia("(pointer: coarse)").matches}
        finalFocus={anchor}
        className="w-80 gap-0 rounded-(--r-pop) bg-(--raised) p-0 text-(length:--fs) shadow-(--e-pop) ring-0">
        <div className="flex flex-col gap-(--s5) p-(--s5)">
          <SearchField
            aria-label="Filter sessions"
            placeholder="Filter sessions"
            value={filter.q}
            onChange={(event) => onFilter({ q: event.target.value })}
          />
          <div className="flex items-center gap-(--s4)">
            <span className="flex-1 text-(--fg2)">Group by</span>
            <SegmentedControl value={[group]} onValueChange={([next]) => (next === "machine" || next === "project") && onGroup(next)} aria-label="Group sessions by">
              <SegmentedControlItem value="project">Project</SegmentedControlItem>
              <SegmentedControlItem value="machine">Machine</SegmentedControlItem>
            </SegmentedControl>
          </div>
        </div>

        <div className="flex flex-col gap-(--s5) border-t-[.5px] border-(--line) p-(--s5)">
          <FilterFacet label="Status" active={filter.statuses.length > 0} onClear={() => onFilter({ statuses: [] })}>
            {shownStatuses.map((status) => (
              <FilterChip key={status} pressed={filter.statuses.includes(status)} count={byStatus.get(status) ?? 0}
                onClick={() => onFilter({ statuses: toggled(filter.statuses, status) })}>
                {status === "needs" && <span aria-hidden className="size-1.5 rounded-full bg-(--warn)" />}
                {statusLabels[status]}
              </FilterChip>
            ))}
          </FilterFacet>
          {agents.length > 1 && (
            <FilterFacet label="Agent" active={filter.agents.length > 0} onClear={() => onFilter({ agents: [] })}>
              {agents.map((agent) => (
                <FilterChip key={agent} pressed={filter.agents.includes(agent)} count={byAgent.get(agent) ?? 0}
                  onClick={() => onFilter({ agents: toggled(filter.agents, agent) })}>
                  {harnessName(agent)}
                </FilterChip>
              ))}
            </FilterFacet>
          )}
          {machines.length > 1 && (
            <div className="flex items-center gap-(--s4)">
              <span className="flex-1 text-(--fg2)">Machine</span>
              <PopupSelect label="Machine" value={filter.host ?? "all"} onValueChange={(value) => onFilter({ host: value === "all" ? undefined : value })}
                items={[{ value: "all", label: "All machines" }, ...machines.map((machine) => ({ value: machine.id, label: machine.name }))]} />
            </div>
          )}
        </div>

        <div className="flex items-center gap-(--s2) border-t-[.5px] border-(--line) px-(--s3) py-(--s3)">
          <Button variant="ghost" size="sm" disabled={refresh.pending} onClick={refresh.run}>
            <RefreshCw aria-hidden className={cn("size-3.5", refresh.pending && "animate-spin motion-reduce:animate-none")} />
            {refresh.pending ? "Refreshing…" : "Refresh"}
          </Button>
          <Button variant="ghost" size="sm" className="ms-auto" disabled={isDefault} onClick={onReset}>
            <RotateCcw aria-hidden className="size-3.5" />
            Reset to default
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
