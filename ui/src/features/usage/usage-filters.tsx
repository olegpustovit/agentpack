import { useRef, useState, type KeyboardEvent, type RefObject } from "react"
import { FilterChip, FilterFacet } from "@/components/kit/filters"
import { SearchField } from "@/components/kit/list"
import { Popover, PopoverContent } from "@/components/ui/popover"
import { fmt, inView, metrics, reportsCost, totalsBy, type Metric, type UsageHour, type View } from "./model"
import type { Names } from "./use-usage"

const facets = [
  { kind: "project", label: "Project", key: (row: UsageHour) => row.folder_id },
  { kind: "machine", label: "Machine", key: (row: UsageHour) => row.node_id },
  { kind: "agent", label: "Agent", key: (row: UsageHour) => row.harness },
] as const
const TOP = 8

// The Usage view's filters: one value per dimension, each chip showing what it would show.
export default function UsageFilters({ anchor, open, onOpenChange, view, rows, metric, names, onFilter }: {
  anchor: RefObject<HTMLButtonElement | null>
  open: boolean
  onOpenChange: (open: boolean) => void
  view: View
  // Every row of the range, before filtering.
  rows: UsageHour[]
  metric: Metric
  names: Names
  onFilter: (patch: View) => void
}) {
  const [query, setQuery] = useState("")
  const [all, setAll] = useState(false)
  const search = useRef<HTMLDivElement>(null)
  const needle = query.trim().toLowerCase()
  const unit = metrics[metric].kind

  const options = facets.map((facet) => {
    // Order by the whole range, so chips stay put while filtering.
    const order = [...totalsBy(rows, metric, facet.key)].sort((a, b) => b[1] - a[1]).map(([id]) => id)
    const chosen = view[facet.kind]
    if (chosen !== undefined && !order.includes(chosen)) order.unshift(chosen)
    // Faceted: each value counts within the other active filters.
    const others = rows.filter((row) => inView(row, { ...view, [facet.kind]: undefined }))
    const totals = totalsBy(others, metric, facet.key)
    const value = (id: string) => {
      const total = totals.get(id) ?? 0
      if (unit === "cost" && total === 0 && !reportsCost(others.filter((row) => facet.key(row) === id))) return "—"
      return total ? fmt(total, unit) : 0
    }
    let ids = order
    if (facet.kind === "project") {
      ids = needle ? order.filter((id) => names.project(id).toLowerCase().includes(needle)) : all ? order : order.slice(0, TOP)
      if (!needle && chosen !== undefined && !ids.includes(chosen)) ids = [...ids, chosen]
    }
    return { ...facet, ids, total: order.length, chosen, value }
  })

  // ⌘F stays inside the popover: the sidebar's field would otherwise take focus and close it.
  function onKeyDown(event: KeyboardEvent) {
    if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "f") return
    event.preventDefault()
    event.stopPropagation()
    search.current?.querySelector("input")?.select()
  }

  return (
    <Popover open={open} onOpenChange={(next, details) => {
      // The lazy popup uses an external anchor. Let the button handle its own presses.
      if (details.reason === "outside-press" && details.event.target instanceof Node && anchor.current?.contains(details.event.target)) {
        details.cancel()
        return
      }
      if (!next) setQuery("")
      onOpenChange(next)
    }}>
      <PopoverContent anchor={anchor} align="end" sideOffset={6} aria-label="Usage filters" onKeyDown={onKeyDown}
        // Focus the search field, except on touch, where it would raise the keyboard over the filters.
        initialFocus={(type) => type !== "touch" && !matchMedia("(pointer: coarse)").matches}
        finalFocus={anchor}
        className="max-h-(--available-height) w-80 gap-(--s5) overflow-y-auto rounded-(--r-pop) bg-(--raised) p-(--s5) text-(length:--fs) shadow-(--e-pop) ring-0">
        <div ref={search} className="flex shrink-0">
          <SearchField
            aria-label="Search projects"
            placeholder="Search projects"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              const first = options[0]!.ids[0]
              if (event.key === "Enter" && needle && first !== undefined) onFilter({ project: first })
            }}
          />
        </div>
        {options.map((facet) => (
          <FilterFacet key={facet.kind} label={facet.label} active={facet.chosen !== undefined} onClear={() => onFilter({ [facet.kind]: undefined })}>
            {facet.ids.map((id) => (
              <FilterChip key={id} pressed={facet.chosen === id} count={facet.value(id)} onClick={() => onFilter({ [facet.kind]: facet.chosen === id ? undefined : id })}>
                {names.of(facet.kind, id)}
              </FilterChip>
            ))}
            {facet.ids.length === 0 && <span className="text-(length:--fs-sm) text-(--fg2)">{needle ? "No matching projects" : "Nothing in this range"}</span>}
            {facet.kind === "project" && !needle && !all && facet.total > TOP && (
              <button type="button" onClick={() => setAll(true)} className="h-(--ctl-h-sm) rounded-(--r-xs) px-(--s2) text-(length:--fs-sm) text-(--accent) hover:underline">{`Show all (${facet.total})`}</button>
            )}
          </FilterFacet>
        ))}
      </PopoverContent>
    </Popover>
  )
}
