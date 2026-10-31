import { useDeferredValue, useEffect, useRef, useState } from "react"
import { Link, getRouteApi, useMatchRoute } from "@tanstack/react-router"
import { Boxes, Plug, Puzzle, Sparkles, type LucideIcon } from "lucide-react"
import { PopupSelect } from "@/components/kit/forms"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"
import { GroupHeader, SearchField, SegmentedControl, SegmentedControlItem, SourceList, SourceRow } from "@/components/kit/list"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import { plural, shortHash } from "@/lib/format"
import { agentpacksQuery, packagesQuery } from "./queries"
import { AddPackageSheet } from "./add-package-sheet"
import { PendingInstalls } from "./pending-installs"
import {
  arrangeLibrary,
  buildLibrary,
  parseFilter,
  parseSort,
  type LibraryFilter,
  type LibraryItem,
  type LibrarySort,
  type LibraryType,
} from "./model"

const route = getRouteApi("/_app/library")

const filterTabs: { value: LibraryFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "skill", label: "Skills" },
  { value: "plugin", label: "Plugins" },
  { value: "mcp", label: "MCP" },
  { value: "agentpack", label: "Packs" },
]

const sortItems: { value: LibrarySort; label: string }[] = [
  { value: "type", label: "Type" },
  { value: "name", label: "Name" },
  { value: "updated", label: "Recently updated" },
  { value: "machine", label: "Machine" },
]

const glyphs: Record<LibraryType, LucideIcon> = { skill: Sparkles, plugin: Puzzle, mcp: Plug, agentpack: Boxes }

// Second line. Only real fields: commit, project count, machines (or launcher and pin for MCP).
function detailLine(item: LibraryItem): string {
  const projects = plural(item.projects.length, "project")
  if (item.type === "mcp") {
    return [item.launcher, item.version ? `pinned ${item.version}` : "unpinned", projects].filter(Boolean).join(" · ")
  }
  if (item.type === "agentpack") {
    return [shortHash(item.commit), plural(item.revisions, "revision"), item.machines.join(", ")].filter(Boolean).join(" · ")
  }
  return [shortHash(item.commit), projects, item.machines.join(", ")].filter(Boolean).join(" · ")
}

function LibraryRow({ item }: { item: LibraryItem }) {
  const matchRoute = useMatchRoute()
  const Glyph = glyphs[item.type]
  const selected =
    item.type === "agentpack"
      ? matchRoute({ to: "/library/agentpack/$id", params: { id: item.id } })
      : item.type === "mcp"
        ? matchRoute({ to: "/library/mcp/$name", params: { name: item.name } })
        : matchRoute({ to: "/library/package/$", params: { _splat: item.module } })
  const link =
    item.type === "agentpack" ? (
      <Link to="/library/agentpack/$id" params={{ id: item.id }} search={true} />
    ) : item.type === "mcp" ? (
      <Link to="/library/mcp/$name" params={{ name: item.name }} search={true} />
    ) : (
      <Link to="/library/package/$" params={{ _splat: item.module }} search={true} />
    )

  return (
    <SourceRow render={link} selected={Boolean(selected)} className="h-(--row-h-2) max-[760px]:h-11">
      <Glyph aria-hidden className="size-(--s4) shrink-0 text-(--fg2) group-data-selected/row:text-(--accent-fg)" />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate">{item.name}</span>
        <span className="truncate text-(--fs-sm) text-(--fg2) group-data-selected/row:text-(--accent-fg)">{detailLine(item)}</span>
      </span>
    </SourceRow>
  )
}

export function LibraryList() {
  const machines = useMachines()
  const packages = useMachineQueries(packagesQuery)
  const agentpacks = useMachineQueries(agentpacksQuery)
  const projects = useMachineQueries(projectsQuery)
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const adding = search.add === "1"

  const filter = parseFilter(search.type)
  const sort = parseSort(search.sort)
  const query = search.q ?? ""
  // The URL trails typing by a render, so the input keeps its own draft and only follows the URL while unfocused.
  const [draft, setDraft] = useState(query)
  const typing = useRef(false)
  useEffect(() => {
    if (!typing.current) setDraft(query)
  }, [query])
  const deferredQuery = useDeferredValue(query)

  const items = buildLibrary(
    machines.map((machine) => ({
      machine,
      packages: packages.byMachine[machine.id]?.data ?? [],
      agentpacks: agentpacks.byMachine[machine.id]?.data ?? [],
      projects: projects.byMachine[machine.id]?.data ?? [],
    })),
  )
  const view = arrangeLibrary(items, { filter, sort, query: deferredQuery })
  const loading = packages.firstLoadPending || agentpacks.firstLoadPending || projects.firstLoadPending
  const queries = [packages, agentpacks, projects]
  const offlineWithData = machines.filter(
    (machine) => !machine.online && queries.some((entry) => entry.byMachine[machine.id]?.data !== undefined),
  )

  // Search, type and sort live in the URL. Replace, so each keystroke does not add history.
  function patch(values: { q?: string; type?: string; sort?: string }) {
    void navigate({ search: (prev) => ({ ...prev, ...values }), replace: true })
  }

  function chooseFilter(value: unknown[]) {
    const next = value[0]
    // Clicking the active tab empties the value. Keep the filter then.
    if (typeof next !== "string") return
    patch({ type: next === "all" ? undefined : next })
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 flex-col gap-(--s2) px-(--s3) pt-(--s2) pb-(--s1)">
        <SegmentedControl value={[filter]} onValueChange={chooseFilter} aria-label="Type" className="w-full">
          {filterTabs.map((tab) => (
            <SegmentedControlItem key={tab.value} value={tab.value} className="min-w-0 flex-1 px-(--s2)">
              {tab.label}
              <span className="ms-(--s1) text-(--fg3)">{view.counts[tab.value]}</span>
            </SegmentedControlItem>
          ))}
        </SegmentedControl>
        <div className="flex min-w-0 items-center gap-(--s2)">
          <SearchField
            aria-label="Search library"
            placeholder="Search"
            value={draft}
            onFocus={() => {
              typing.current = true
            }}
            onBlur={() => {
              typing.current = false
            }}
            onChange={(event) => {
              setDraft(event.target.value)
              patch({ q: event.target.value || undefined })
            }}
            className="min-w-0 flex-1"
          />
          <PopupSelect
            label="Sort"
            value={sort}
            items={sortItems}
            onValueChange={(next) => patch({ sort: next === "type" ? undefined : next })}
          />
        </div>
        <div className="flex justify-end">
          <Button type="button" variant="ghost" size="sm" onClick={() => void navigate({ search: (prev) => ({ ...prev, add: "1" }) })}>
            Add package…
          </Button>
        </div>
      </div>

      <SourceList aria-label="Library" className="min-h-0 flex-1">
        <PendingInstalls />
        {machines.map((machine) => {
          if (queries.some((entry) => entry.byMachine[machine.id]?.needsUpdate)) {
            return <Banner key={machine.id} tone="warn">{`${machine.name} needs an update to show its library.`}</Banner>
          }
          const error = queries.map((entry) => entry.errors[machine.id]).find(Boolean)
          if (error) {
            return <Banner key={machine.id} tone="danger">{`Couldn't load library from ${machine.name}: ${error.message}`}</Banner>
          }
          return null
        })}
        {offlineWithData.length > 0 && (
          <Banner>
            {`${offlineWithData.map((machine) => machine.name).join(", ")} ${offlineWithData.length === 1 ? "is" : "are"} offline. Showing last known library.`}
          </Banner>
        )}
        {loading && [0, 1, 2].map((index) => <RowSkeleton key={index} lines={2} />)}
        {!loading && view.groups.length === 0 &&
          (items.length === 0 ? (
            <Empty title="Nothing in the library yet">
              Skills, plugins, MCP servers and agentpacks from your machines appear here.
            </Empty>
          ) : (
            <Empty title="No matches">Try another search or type.</Empty>
          ))}
        {view.groups.map((group) => (
          <div key={group.key}>
            {group.label && (
              <GroupHeader>
                <span className="min-w-0 flex-1 truncate">{group.label}</span>
                <span className="shrink-0 font-normal text-(--fg3)">{group.items.length}</span>
              </GroupHeader>
            )}
            {group.items.map((item) => (
              <LibraryRow key={item.key} item={item} />
            ))}
          </div>
        ))}
      </SourceList>
      <AddPackageSheet
        open={adding}
        onOpenChange={(open) => {
          if (!open) void navigate({ search: (prev) => ({ ...prev, add: undefined }), replace: true })
        }}
      />
    </div>
  )
}
