import { getRouteApi } from "@tanstack/react-router"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useState, type ReactNode } from "react"
import { Puzzle, RefreshCw, Sparkles } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Hash, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { Banner, Empty } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"
import { plural, relativeTime, shortHash } from "@/lib/format"
import { projectsQuery } from "@/features/sessions/queries"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { installedIn, packageRecord, packageVersions, type InstalledRow, type VersionRow } from "./inspect"
import { errorText, installPackage } from "./mutations"
import { Failures, MissingItem } from "./inspector-state"
import type { PackageItem } from "./model"
import { useLatest, useLibrary } from "./use-library"
import { InstallSheet } from "./install-sheet"

const route = getRouteApi("/_app/library/package/$")

const sourceName = (module: string) => module.replace(/^github\.com\//i, "")

// A table cannot fit a phone's width, so each row becomes two lines and its action.
function PhoneRow({ title, detail, children }: { title: string; detail: string; children: ReactNode }) {
  return (
    <li className="flex min-w-0 items-center gap-(--s3) border-b-[0.5px] border-(--sep) py-(--s2)">
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate">{title}</span>
        <span className="truncate text-(length:--fs-sm) text-(--fg2)">{detail}</span>
      </span>
      <span className="shrink-0 text-(length:--fs-sm) text-(--fg2)">{children}</span>
    </li>
  )
}

export function PackageInspector({ module }: { module: string }) {
  const isPhone = useIsPhone()
  const { inputs, items, loading, failures } = useLibrary()
  const item = items.find(
    (entry): entry is PackageItem =>
      (entry.type === "skill" || entry.type === "plugin") && entry.sources.some((source) => source.toLowerCase() === module.toLowerCase()),
  )
  const [picked, setPicked] = useState<{ module: string; commit: string }>()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const queryClient = useQueryClient()
  const latest = useLatest(inputs, item?.sources ?? [], Boolean(item))
  const update = useMutation({
    mutationFn: async (rows: { row: InstalledRow; commit: string }[]) => {
      const failed: string[] = []
      for (const { row, commit } of rows) {
        try {
          await installPackage(row.machineId, row.directory, `${row.module}@${commit}`, true)
          void queryClient.invalidateQueries({ queryKey: projectsQuery(row.machineId).queryKey })
        } catch (error) {
          failed.push(`${row.project}: ${errorText(error)}`)
        }
      }
      return { failed, total: rows.length }
    },
    onSuccess: ({ failed, total }) => {
      if (failed.length) hud.error(`Couldn't update ${failed.join("; ")}`)
      else hud.success(total === 1 ? "Updated 1 project" : `Updated ${total} projects`)
    },
  })
  if (!item) {
    return (
      <MissingItem loading={loading} failures={failures} title="Package not found">
        {`No machine's library has ${module}. A machine that is offline may still hold it.`}
      </MissingItem>
    )
  }

  const latestOf = (source: string) => latest.byModule[source.toLowerCase()]
  const known = packageVersions(inputs, item.sources)
  // A newer version nobody has fetched yet still belongs in the list.
  const installed = installedIn(inputs, item.sources)
  const chosen = picked ?? { module: item.module, commit: latestOf(item.module)?.commit ?? item.commit }
  const record = packageRecord(inputs, chosen.module, chosen.commit) ?? packageRecord(inputs, item.module, item.commit)
  const behind = installed.flatMap((row) => {
    const newest = latestOf(row.module)
    return newest && !newest.current.includes(row.commit) ? [{ row, commit: newest.commit }] : []
  })
  const updatable = behind.filter(({ row }) => row.online)
  const kind = item.type === "plugin" ? "Plugin" : "Skill"
  const Glyph = item.type === "plugin" ? Puzzle : Sparkles
  const openInstall = (version: { module: string; commit: string }) => {
    setPicked(version)
    void navigate({ search: (prev) => ({ ...prev, install: "1" }) })
  }

  const fresh = new Map<string, VersionRow>()
  for (const source of item.sources) {
    const newest = latestOf(source)
    // A moved repository answers under both names with one commit, which is one version.
    if (newest && !known.some((row) => row.commit === newest.commit) && !fresh.has(newest.commit)) {
      fresh.set(newest.commit, { key: `${source}@${newest.commit}`, module: source, commit: newest.commit, projects: 0, machines: [], seen: "" })
    }
  }
  const versions = [...fresh.values(), ...known]
  const latestLabel = (version: VersionRow) => {
    const newest = latestOf(version.module)
    if (newest?.commit !== version.commit) return ""
    return newest.committed_at ? `Latest, ${relativeTime(new Date(newest.committed_at))}` : "Latest"
  }
  const statusOf = (row: InstalledRow) => {
    const target = behind.find((entry) => entry.row.key === row.key)
    if (target) {
      return (
        <Button size="sm" disabled={!row.online || update.isPending} onClick={() => update.mutate([target])}>
          {`Update to ${shortHash(target.commit)}`}
        </Button>
      )
    }
    return latestOf(row.module) ? "Latest" : latest.checking ? "Checking…" : "Unknown"
  }
  const installVersion = (version: VersionRow) => (
    <Button size="sm" variant="ghost" onClick={() => openInstall(version)}>
      {`Install ${shortHash(version.commit)}…`}
    </Button>
  )
  const used = (version: VersionRow) => (version.projects ? `${plural(version.projects, "project")} · ${version.machines.join(", ")}` : "Not installed")
  return (
    <Inspector>
      <Failures failures={failures} />
      <InspectorHead
        icon={<Glyph aria-hidden />}
        title={item.name}
        subtitle={`${kind} · ${plural(versions.length, "version")} · ${plural(item.projects.length, "project")}`}
      >
        <Button type="button" variant="ghost" size="sm" disabled={latest.checking} onClick={latest.refetch}>
          <RefreshCw aria-hidden className={cn(latest.checking && "animate-spin motion-reduce:animate-none")} />
          Check for updates
        </Button>
        {updatable.length > 0 && (
          <Button type="button" size="sm" disabled={update.isPending} onClick={() => update.mutate(updatable)}>
            {`Update ${plural(updatable.length, "project")}`}
          </Button>
        )}
        <Button type="button" variant="primary" onClick={() => openInstall(chosen)}>
          Install…
        </Button>
      </InspectorHead>
      {latest.failed && <Banner tone="warn">Couldn't reach GitHub for the latest version. Showing the versions your machines know.</Banner>}
      <Section title="Installed in" className="col-span-full">
        {installed.length === 0 ? (
          <Empty title="Not installed yet">No project on any machine pins this package.</Empty>
        ) : (
          <>
            <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">{plural(installed.length, "project")}</p>
            {isPhone ? (
              <ul aria-label="Installed in">
                {installed.map((row) => (
                  <PhoneRow key={row.key} title={row.project} detail={`${row.machine} · ${shortHash(row.commit)}`}>
                    {statusOf(row)}
                  </PhoneRow>
                ))}
              </ul>
            ) : (
              <DataGrid aria-label="Installed in">
                <DataGridHead>
                  <tr>
                    <DataGridHeaderCell className="w-40">Machine</DataGridHeaderCell>
                    <DataGridHeaderCell>Project</DataGridHeaderCell>
                    <DataGridHeaderCell className="w-28">Version</DataGridHeaderCell>
                    <DataGridHeaderCell className="w-44">Status</DataGridHeaderCell>
                  </tr>
                </DataGridHead>
                <tbody>
                  {installed.map((row) => (
                    <DataGridRow key={row.key}>
                      <DataGridCell>{row.machine}</DataGridCell>
                      <DataGridCell title={row.directory}>{row.project}</DataGridCell>
                      <DataGridCell>
                        <Hash value={row.commit} />
                      </DataGridCell>
                      <DataGridCell>{statusOf(row)}</DataGridCell>
                    </DataGridRow>
                  ))}
                </tbody>
              </DataGrid>
            )}
          </>
        )}
      </Section>
      <Section title="Versions" className="col-span-full">
        {isPhone ? (
          <ul aria-label="Versions">
            {versions.map((version) => (
              <PhoneRow key={version.key} title={[shortHash(version.commit), latestLabel(version)].filter(Boolean).join(" · ")} detail={`${used(version)} · ${sourceName(version.module)}`}>
                {installVersion(version)}
              </PhoneRow>
            ))}
          </ul>
        ) : (
          <DataGrid aria-label="Versions">
            <DataGridHead>
              <tr>
                <DataGridHeaderCell className="w-64">Version</DataGridHeaderCell>
                <DataGridHeaderCell>Source</DataGridHeaderCell>
                <DataGridHeaderCell className="w-56">Used by</DataGridHeaderCell>
                <DataGridHeaderCell className="w-36" />
              </tr>
            </DataGridHead>
            <tbody>
              {versions.map((version) => (
                <DataGridRow key={version.key}>
                  <DataGridCell>
                    <Hash value={version.commit} />
                    {latestLabel(version) && <span className="text-(--fg2)">{` · ${latestLabel(version)}`}</span>}
                  </DataGridCell>
                  <DataGridCell title={version.module}>{sourceName(version.module)}</DataGridCell>
                  <DataGridCell>{used(version)}</DataGridCell>
                  <DataGridCell>{installVersion(version)}</DataGridCell>
                </DataGridRow>
              ))}
            </tbody>
          </DataGrid>
        )}
      </Section>
      <Section title="Details" className="col-span-full">
        <dl className="flex flex-col gap-(--s3)">
          <KeyValue label={item.sources.length > 1 ? "Sources" : "Source"}>
            <span className="flex min-w-0 flex-col">
              {item.sources.map((source) => (
                <code key={source} className="break-all">{source}</code>
              ))}
            </span>
          </KeyValue>
          <KeyValue label="Kind">{kind}</KeyValue>
          <KeyValue label="Repository">
            {record?.url ? <a href={record.url} target="_blank" rel="noreferrer" className="break-all text-(--accent) hover:underline">{record.url}</a> : "—"}
          </KeyValue>
          <KeyValue label="Content hash">{record?.content_hash ? <Hash value={record.content_hash} /> : "—"}</KeyValue>
        </dl>
      </Section>
      <InstallSheet
        item={{ module: chosen.module, commit: chosen.commit, name: item.name, cache_key: record?.commit === chosen.commit ? record.cache_key : "", content_hash: record?.commit === chosen.commit ? record.content_hash : undefined }}
        open={search.install === "1"}
        onOpenChange={(open) => {
          if (!open) void navigate({ search: (prev) => ({ ...prev, install: undefined }), replace: true })
        }}
      />
    </Inspector>
  )
}
