import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Compass, Copy, Ellipsis, Plug, Puzzle } from "lucide-react"
import { Inspector, InspectorHead } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { SearchField, SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import type { CatalogItem } from "@/lib/api-types"
import { copyText } from "@/lib/hud"
import { plural } from "@/lib/format"
import { InstallProjectMenu } from "./install-project-menu"
import type { MCPItem, PackageItem } from "./model"
import { catalogQuery, useSettled } from "./queries"
import { useLibrary } from "./use-library"

type Kind = "all" | CatalogItem["kind"]
const kinds: { value: Kind; label: string }[] = [
  { value: "all", label: "All" },
  { value: "plugin", label: "Plugins" },
  { value: "mcp", label: "MCP servers" },
]

const moduleOf = (spec: string) => spec.split("@")[0]!.toLowerCase()

export function DiscoverView() {
  // Discover keeps its own catalog search independent of the Library sidebar's `q` filter.
  const [catalogQueryText, setCatalogQueryText] = useState("")
  const query = useSettled(catalogQueryText.trim())
  const catalog = useQuery(catalogQuery(query))
  const { items: library } = useLibrary()
  const [kind, setKind] = useState<Kind>("all")

  // Map catalog entries to existing Library items so installed rows link to their installations and still offer Install.
  const packagesByModule = new Map<string, PackageItem>()
  const mcpsByName = new Map<string, MCPItem>()
  for (const item of library) {
    if (item.type === "plugin" || item.type === "skill") {
      for (const module of item.sources) packagesByModule.set(module.toLowerCase(), item)
    }
    if (item.type === "mcp") mcpsByName.set(item.name, item)
  }

  function matchLibrary(item: CatalogItem): PackageItem | MCPItem | undefined {
    if (item.spec) return packagesByModule.get(moduleOf(item.spec))
    return mcpsByName.get(item.name)
  }

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
      <div className="col-span-full flex min-w-0 items-center gap-(--s2)">
        <SearchField
          aria-label="Search catalog"
          placeholder="Search catalog"
          className="max-[760px]:h-11"
          value={catalogQueryText}
          onChange={(event) => setCatalogQueryText(event.target.value)}
        />
        {catalogQueryText && (
          <Button type="button" variant="ghost" size="sm" className="shrink-0 max-[760px]:h-11" onClick={() => setCatalogQueryText("")}>
            Clear
          </Button>
        )}
      </div>
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
          const existing = matchLibrary(item)
          const count = existing?.projects.length ?? 0
          const spec = existing && existing.type !== "mcp" && count > 0 ? `${existing.module}@${existing.commit}` : item.spec
          const target = spec ? { spec } : item.server ? { server: item.server } : undefined
          const Glyph = item.kind === "mcp" ? Plug : Puzzle
          return (
            <li key={`${item.source}\0${item.name}`} className="flex min-w-0 flex-wrap items-start gap-(--s3) border-b-[0.5px] border-(--sep) py-(--s2)">
              <span className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-(--r-row) bg-(--track) text-(--fg2)">
                <Glyph aria-hidden className="size-(--fs)" />
              </span>
              <span className="flex min-w-44 flex-1 flex-col gap-0.5">
                <span className="break-words">
                  <span className="font-semibold">{item.name}</span>{" "}
                  <span className="text-(length:--fs-xs) text-(--fg2)">{[item.kind === "mcp" ? "MCP" : "Plugin", item.publisher].filter(Boolean).join(" · ")}</span>
                </span>
                {item.description && (
                  <span className="break-words text-(length:--fs-sm) text-(--fg2)">{item.description}</span>
                )}
              </span>
              <span className="flex shrink-0 flex-wrap items-center gap-(--s2)">
                {count > 0 && existing && (
                  existing.type === "mcp" ? (
                    <Link
                      to="/library/mcp/$name"
                      params={{ name: existing.name }}
                      search={true}
                      title="View installations"
                      className="text-(length:--fs-sm) text-(--accent-text) underline-offset-2 hover:underline"
                    >
                      {`In ${plural(count, "project")}`}
                    </Link>
                  ) : (
                    <Link
                      to="/library/package/$"
                      params={{ _splat: existing.module }}
                      search={true}
                      title="View installations"
                      className="text-(length:--fs-sm) text-(--accent-text) underline-offset-2 hover:underline"
                    >
                      {`In ${plural(count, "project")}`}
                    </Link>
                  )
                )}
                {target && (
                  <InstallProjectMenu
                    name={item.name}
                    target={target}
                    size={count > 0 ? "sm" : undefined}
                    aria-label={count > 0 ? `Install ${item.name} in another project` : `Install ${item.name}…`}
                  >
                    {count > 0 ? "Install in another project" : "Install…"}
                  </InstallProjectMenu>
                )}
                {item.command && (
                  <Menu>
                    <MenuTrigger
                      render={
                        <Button type="button" variant="ghost" size="icon" aria-label={`More actions for ${item.name}`}>
                          <Ellipsis aria-hidden className="size-(--fs)" />
                        </Button>
                      }
                    />
                    <MenuContent align="end">
                      <MenuItem icon={<Copy aria-hidden />} onClick={() => copy(item.command!)}>
                        Copy command
                      </MenuItem>
                    </MenuContent>
                  </Menu>
                )}
              </span>
            </li>
          )
        })}
      </ul>
      {!catalog.isPending && rows.length === 0 && (
        <Empty title={needle ? "No matches" : "Nothing to discover"} className="col-span-full">
          {needle ? (
            <div className="flex flex-col items-center gap-(--s3)">
              <span>Try another search.</span>
              <Button type="button" size="sm" onClick={() => setCatalogQueryText("")}>
                Clear search
              </Button>
            </div>
          ) : (
            "The catalog sources returned nothing."
          )}
        </Empty>
      )}
    </Inspector>
  )
}

function copy(command: string) {
  void copyText(command, `Couldn't copy. Run: ${command}`)
}
