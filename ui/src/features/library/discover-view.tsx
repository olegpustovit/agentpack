import { useEffect, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { Compass, Copy, Plug, Puzzle } from "lucide-react"
import { Inspector, InspectorHead } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import type { CatalogItem } from "@/lib/api-types"
import { hud } from "@/lib/hud"
import { plural } from "@/lib/format"
import { catalogQuery } from "./queries"
import { useLibrary } from "./use-library"

const route = getRouteApi("/_app/library/discover")

type Kind = "all" | CatalogItem["kind"]
const kinds: { value: Kind; label: string }[] = [
  { value: "all", label: "All" },
  { value: "plugin", label: "Plugins" },
  { value: "mcp", label: "MCP servers" },
]

const moduleOf = (spec: string) => spec.split("@")[0]!.toLowerCase()

// The list's search field filters the catalog too. The registry is searched server-side, so typing settles first.
function useSettled(value: string): string {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), 300)
    return () => clearTimeout(timer)
  }, [value])
  return settled
}

export function DiscoverView() {
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const query = useSettled((search.q ?? "").trim())
  const catalog = useQuery(catalogQuery(query))
  const { items: library } = useLibrary()
  const [kind, setKind] = useState<Kind>("all")

  // How many projects already pin each catalog entry, so installed ones say so instead of offering Install.
  const projects = new Map<string, number>()
  for (const item of library) {
    if (item.type === "plugin" || item.type === "skill") for (const module of item.sources) projects.set(module.toLowerCase(), item.projects.length)
    if (item.type === "mcp") projects.set(`mcp:${item.name}`, item.projects.length)
  }
  const used = (item: CatalogItem) => (item.spec ? projects.get(moduleOf(item.spec)) : projects.get(`mcp:${item.name}`)) ?? 0

  const needle = query.toLowerCase()
  // MCP rows already match the search on the server; plugins are one small file filtered here.
  const rows = (catalog.data?.items ?? []).filter(
    (item) => (kind === "all" || item.kind === kind) && (item.kind === "mcp" || !needle || `${item.name} ${item.description ?? ""}`.toLowerCase().includes(needle)),
  )
  const sources = catalog.data?.sources ?? []

  return (
    <Inspector className="min-[1260px]:grid-cols-1">
      <InspectorHead
        icon={<Compass aria-hidden />}
        title="Discover"
        subtitle="From the marketplaces and registries your machines already trust. Installing adds a pin to a project's agentpack.toml."
      />
      {catalog.error && <Banner tone="danger" className="col-span-full">{`Couldn't load the catalog: ${catalog.error.message}`}</Banner>}
      {sources.filter((source) => source.error).map((source) => (
        <Banner key={source.name} tone="warn" className="col-span-full">{`Couldn't reach ${source.name}: ${source.error}`}</Banner>
      ))}
      <div className="col-span-full flex min-w-0 flex-wrap items-center justify-between gap-(--s3)">
        <SegmentedControl value={[kind]} onValueChange={(value) => typeof value[0] === "string" && setKind(value[0] as Kind)} aria-label="Catalog type">
          {kinds.map((entry) => (
            <SegmentedControlItem key={entry.value} value={entry.value}>
              {entry.label}
            </SegmentedControlItem>
          ))}
        </SegmentedControl>
        <span className="text-(length:--fs-sm) text-(--fg2)">
          {sources.map((source) => `${source.name} ${source.count}`).join(" · ")}
        </span>
      </div>
      <ul aria-label="Catalog" aria-busy={catalog.isFetching} className="col-span-full flex min-w-0 flex-col">
        {catalog.isPending && [0, 1, 2].map((index) => <RowSkeleton key={index} lines={2} />)}
        {rows.map((item) => {
          const count = used(item)
          const Glyph = item.kind === "mcp" ? Plug : Puzzle
          return (
            <li key={`${item.source}\0${item.name}`} className="flex min-w-0 items-center gap-(--s3) border-b-[0.5px] border-(--sep) py-(--s2)">
              <span className="grid size-7 shrink-0 place-items-center rounded-(--r-row) bg-(--track) text-(--fg2)">
                <Glyph aria-hidden className="size-(--fs)" />
              </span>
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate">
                  <span className="font-semibold">{item.name}</span>{" "}
                  <span className="text-(length:--fs-xs) text-(--fg2)">{[item.kind === "mcp" ? "MCP" : "Plugin", item.publisher].filter(Boolean).join(" · ")}</span>
                </span>
                {item.description && <span className="truncate text-(length:--fs-sm) text-(--fg2)" title={item.description}>{item.description}</span>}
              </span>
              {count > 0 ? (
                <span className="shrink-0 text-(length:--fs-sm) text-(--fg2)">{`In ${plural(count, "project")}`}</span>
              ) : item.spec ? (
                <Button type="button" aria-label={`Install ${item.name}…`} onClick={() => void navigate({ search: (prev) => ({ ...prev, add: item.spec }) })}>
                  Install…
                </Button>
              ) : item.command ? (
                <Button type="button" aria-label={`Copy the command that adds ${item.name}`} title={item.command} onClick={() => copy(item.command!)}>
                  <Copy aria-hidden />
                  Copy command
                </Button>
              ) : null}
            </li>
          )
        })}
      </ul>
      {!catalog.isPending && rows.length === 0 && (
        <Empty title={needle ? "No matches" : "Nothing to discover"} className="col-span-full">
          {needle ? "Try another search." : "The catalog sources returned nothing."}
        </Empty>
      )}
    </Inspector>
  )
}

function copy(command: string) {
  // navigator.clipboard is undefined on plain-HTTP gateway origins.
  ;(navigator.clipboard?.writeText(command) ?? Promise.reject()).then(
    () => hud.success("Copied. Run it in the project folder."),
    () => hud.error(`Couldn't copy. Run: ${command}`),
  )
}
