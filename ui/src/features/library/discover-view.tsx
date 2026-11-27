import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, getRouteApi } from "@tanstack/react-router"
import { Compass, Copy, Ellipsis, Plug, Puzzle } from "lucide-react"
import { Inspector, InspectorHead } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { SearchField, SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import type { CatalogItem, MCPServerInput } from "@/lib/api-types"
import { copyText } from "@/lib/hud"
import { plural } from "@/lib/format"
import { InstallMCPSheet } from "./add-package-sheet"
import { packageRecord } from "./inspect"
import { InstallSheet } from "./install-sheet"
import type { InstallItem } from "./install-state"
import type { MCPItem, PackageItem } from "./model"
import { catalogQuery, useSettled } from "./queries"
import { useLibrary } from "./use-library"

const route = getRouteApi("/_app/library/discover")

type Kind = "all" | CatalogItem["kind"]
const kinds: { value: Kind; label: string }[] = [
  { value: "all", label: "All" },
  { value: "plugin", label: "Plugins" },
  { value: "mcp", label: "MCP servers" },
]

const moduleOf = (spec: string) => spec.split("@")[0]!.toLowerCase()

export function DiscoverView() {
  const navigate = route.useNavigate()
  // Discover keeps its own catalog search independent of the Library sidebar's `q` filter.
  const [catalogQueryText, setCatalogQueryText] = useState("")
  const query = useSettled(catalogQueryText.trim())
  const catalog = useQuery(catalogQuery(query))
  const { inputs, items: library } = useLibrary()
  const [kind, setKind] = useState<Kind>("all")
  const [installingMCP, setInstallingMCP] = useState<MCPServerInput>()
  const [installingPackage, setInstallingPackage] = useState<InstallItem>()


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

  function openPackageInstall(pkg: PackageItem) {
    const record = packageRecord(inputs, pkg.module, pkg.commit)
    setInstallingPackage({
      module: pkg.module,
      commit: pkg.commit,
      name: pkg.name,
      cache_key: record?.cache_key ?? "",
      content_hash: record?.content_hash,
    })
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
                {count > 0 ? (
                  existing && (existing.type === "plugin" || existing.type === "skill") ? (
                    <Button type="button" size="sm" aria-label={`Install ${item.name} in another project`} onClick={() => openPackageInstall(existing)}>
                      Install in another project
                    </Button>
                  ) : item.server ? (
                    <Button type="button" size="sm" aria-label={`Install ${item.name} in another project`} onClick={() => setInstallingMCP(item.server)}>
                      Install in another project
                    </Button>
                  ) : item.spec ? (
                    <Button
                      type="button"
                      size="sm"
                      aria-label={`Install ${item.name} in another project`}
                      onClick={() => void navigate({ search: (prev) => ({ ...prev, add: item.spec }) })}
                    >
                      Install in another project
                    </Button>
                  ) : null
                ) : item.spec ? (
                  <Button type="button" aria-label={`Install ${item.name}…`} onClick={() => void navigate({ search: (prev) => ({ ...prev, add: item.spec }) })}>
                    Install…
                  </Button>
                ) : item.server ? (
                  <Button type="button" aria-label={`Install ${item.name}…`} onClick={() => setInstallingMCP(item.server)}>
                    Install…
                  </Button>
                ) : null}
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
      {installingPackage && (
        <InstallSheet
          item={installingPackage}
          open={true}
          onOpenChange={(open) => {
            if (!open) setInstallingPackage(undefined)
          }}
        />
      )}
      <InstallMCPSheet server={installingMCP} onOpenChange={(open) => !open && setInstallingMCP(undefined)} />
    </Inspector>
  )
}

function copy(command: string) {
  void copyText(command, `Couldn't copy. Run: ${command}`)
}
