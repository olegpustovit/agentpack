import { getRouteApi } from "@tanstack/react-router"
import { Boxes } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Hash, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { Empty } from "@/components/kit/feedback"
import { plural } from "@/lib/format"
import { cn } from "@/lib/utils"
import { agentpackRevisions, revisionChanges } from "./inspect"
import { Failures, MissingItem } from "./inspector-state"
import type { AgentpackItem } from "./model"
import { useLibrary } from "./use-library"
import { Button } from "@/components/ui/button"
import { useMachines } from "@/features/machines/queries"
import { RenameSheet, SaveRevisionSheet } from "./save-sheet"
import { RestoreSheet } from "./restore-sheet"

const signs = { add: "+", update: "~", remove: "−" } as const

const route = getRouteApi("/_app/library/agentpack/$id")

// `revision` picks the selected revision; no value, or one this agentpack lacks, shows the newest.
export function AgentpackInspector({
  id,
  revision,
  onRevision,
}: {
  id: string
  revision?: string
  onRevision: (revision: string) => void
}) {
  const { inputs, items, loading, failures } = useLibrary()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const machines = useMachines()
  // Only online machines can take a rename or a new revision now.
  const holders = machines
    .filter((machine) => machine.online && inputs.some((input) => input.machine.id === machine.id && input.agentpacks.some((entry) => entry.agentpack.id === id)))
    .map((machine) => machine.id)
  const pack = items.find((entry): entry is AgentpackItem => entry.type === "agentpack" && entry.id === id)
  if (!pack) {
    return (
      <MissingItem loading={loading} failures={failures} title="Agentpack not found">
        {`No machine's library has this agentpack.`}
      </MissingItem>
    )
  }

  const revisions = agentpackRevisions(inputs, id)
  const index = Math.max(0, revisions.findIndex((entry) => entry.revision.id === revision))
  const selected = revisions[index]
  if (!selected) {
    return (
      <MissingItem loading={loading} failures={failures} title="Revision details are unavailable">
        {`${pack.name} has no saved revisions on any machine.`}
      </MissingItem>
    )
  }

  const { revision: rev } = selected
  const previous = revisions[index + 1]
  const changes = revisionChanges(selected, previous)

  return (
    <Inspector>
      <Failures failures={failures} />
      <InspectorHead
        icon={<Boxes className="size-(--s6)" />}
        title={pack.name}
        subtitle={`${rev.project || "Unknown project"} · saved on ${selected.machine} · ${plural(revisions.length, "revision")}`}
      >
        <Button type="button" variant="ghost" size="sm" onClick={() => void navigate({ search: (prev) => ({ ...prev, save: "1" }) })}>
          Save revision…
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={() => void navigate({ search: (prev) => ({ ...prev, rename: "1" }) })}>
          Rename…
        </Button>
      </InspectorHead>
      <Section title="Details" className="col-span-full">
        <dl className="flex flex-col gap-(--s3)">
          <KeyValue label="Source">
            <code>{rev.project || "—"}</code> on {selected.machine}
          </KeyValue>
          <KeyValue label="Revision">
            <Hash value={rev.id} />
          </KeyValue>
          <KeyValue label="Configuration">{rev.needs_local_configuration ? "Needs local configuration" : "Complete on this machine"}</KeyValue>
        </dl>
      </Section>
      <Section title="History">
        <p className="mb-(--s2) text-(--fs-sm) text-(--fg2)">Newest first</p>
        <ul className="flex flex-col">
          {revisions.map((entry, position) => (
            <li key={entry.revision.id} className="flex items-center gap-(--s3)">
              <button
                type="button"
                aria-pressed={entry.revision.id === rev.id}
                onClick={() => onRevision(entry.revision.id)}
                className={cn(
                  "flex min-w-0 flex-1 flex-col rounded-(--r-row) px-(--s3) py-(--s2) text-left hover:bg-(--hover)",
                  entry.revision.id === rev.id && "bg-(--accent-soft)",
                )}
              >
                <span className="truncate">{entry.revision.label || "Saved revision"}</span>
                <span className="truncate text-(--fs-sm) text-(--fg2)">
                  {new Date(entry.revision.created_at).toLocaleString()} · {entry.machine}
                </span>
              </button>
              {position === 0 && <span className="text-(--fs-sm) text-(--fg3)">Latest</span>}
              <span className="text-(--fs-sm) text-(--fg3)">{plural((entry.revision.package_ids ?? []).length, "package")}</span>
              <Hash value={entry.revision.id} />
            </li>
          ))}
        </ul>
      </Section>
      <Section title={rev.label || "Saved revision"}>
        <p className="mb-(--s2) text-(--fs-sm) text-(--fg2)">{rev.mode || "default"} mode</p>
        <Button type="button" size="sm" className="mb-(--s2)" onClick={() => void navigate({ search: (prev) => ({ ...prev, restore: rev.id }) })}>
          Restore…
        </Button>
        {!previous ? (
          <p className="text-(--fs-sm)">First saved revision.</p>
        ) : changes.length === 0 ? (
          <p className="text-(--fs-sm)">Same packages as previous revision</p>
        ) : (
          <ul aria-label="Changes from previous revision" className="mb-(--s3) flex flex-col gap-(--s1) text-(--fs-sm)">
            {changes.map((change) => (
              <li key={`${change.kind}\0${change.name}`}>
                <span aria-hidden className="inline-block w-4">
                  {signs[change.kind]}
                </span>
                {change.name}
              </li>
            ))}
          </ul>
        )}
        <DataGrid>
          <DataGridHead>
            <tr>
              <DataGridHeaderCell>Package</DataGridHeaderCell>
              <DataGridHeaderCell>Module</DataGridHeaderCell>
              <DataGridHeaderCell className="w-28">Commit</DataGridHeaderCell>
            </tr>
          </DataGridHead>
          <tbody>
            {selected.packages.length === 0 ? (
              <DataGridRow>
                <DataGridCell colSpan={3}>
                  <Empty title="No package pins">This revision pins no packages.</Empty>
                </DataGridCell>
              </DataGridRow>
            ) : (
              selected.packages.map((pkg) => (
                <DataGridRow key={pkg.id}>
                  <DataGridCell>{pkg.name || pkg.module}</DataGridCell>
                  <DataGridCell>
                    <code>{pkg.module}</code>
                  </DataGridCell>
                  <DataGridCell>
                    <Hash value={pkg.commit} />
                  </DataGridCell>
                </DataGridRow>
              ))
            )}
          </tbody>
        </DataGrid>
      </Section>
      <RestoreSheet
        packId={id}
        packName={pack.name}
        revision={rev}
        packages={selected.packages}
        open={search.restore === rev.id}
        onOpenChange={(open) => {
          if (!open) void navigate({ search: (prev) => ({ ...prev, restore: undefined }), replace: true })
        }}
      />
      <SaveRevisionSheet
        packId={id}
        packName={pack.name}
        holders={holders}
        open={search.save === "1"}
        onOpenChange={(open) => {
          if (!open) void navigate({ search: (prev) => ({ ...prev, save: undefined }), replace: true })
        }}
      />
      <RenameSheet
        packId={id}
        packName={pack.name}
        holders={holders}
        open={search.rename === "1"}
        onOpenChange={(open) => {
          if (!open) void navigate({ search: (prev) => ({ ...prev, rename: undefined }), replace: true })
        }}
      />
    </Inspector>
  )
}
