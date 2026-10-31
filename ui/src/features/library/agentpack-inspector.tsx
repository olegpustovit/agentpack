import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { Boxes } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Hash, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { Banner, Empty } from "@/components/kit/feedback"
import { hud } from "@/lib/hud"
import { errorText, resolveConflict } from "./mutations"
import { conflictQuery } from "./queries"
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

// Two machines changed the same value differently. Nothing is overwritten until someone picks.
function ConflictPanel({ machine, id }: { machine: string; id: string }) {
  const queryClient = useQueryClient()
  const { data } = useQuery(conflictQuery(machine, id))
  const [choices, setChoices] = useState<Record<string, string>>({})
  const resolve = useMutation({
    mutationFn: () => resolveConflict(machine, id, data?.heads ?? [], choices),
    onSuccess: () => {
      hud.success("Conflict resolved")
      void queryClient.invalidateQueries({ queryKey: ["m"] })
    },
    onError: (error) => hud.error(`Couldn't resolve the conflict: ${errorText(error)}`),
  })
  if (!data?.fields.length) return null
  const complete = data.fields.every((field) => choices[field.path])
  return (
    <Section title="Conflict" className="col-span-full">
      <Banner tone="warn">Two machines changed this agentpack differently. Choose which value to keep; the other stays in the history.</Banner>
      <div className="flex flex-col">
        {data.fields.map((field) => (
          <div key={field.path} className="flex min-w-0 flex-wrap items-center gap-(--s3) border-b-[0.5px] border-(--sep) py-(--s2)">
            <span className="min-w-40 flex-1 truncate" title={field.name}>{field.name}</span>
            {data.heads.map((head) => (
              <Button key={head} type="button" size="sm" variant={choices[field.path] === head ? "primary" : "ghost"} aria-pressed={choices[field.path] === head} onClick={() => setChoices({ ...choices, [field.path]: head })}>
                <code>{field.values[head]}</code>
              </Button>
            ))}
          </div>
        ))}
      </div>
      <div className="flex justify-end pt-(--s3)">
        <Button type="button" variant="primary" disabled={!complete || resolve.isPending} onClick={() => resolve.mutate()}>
          Resolve conflict
        </Button>
      </div>
    </Section>
  )
}

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
      {pack.conflict && holders[0] && <ConflictPanel machine={holders[0]} id={id} />}
      <InspectorHead
        icon={<Boxes aria-hidden />}
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
        <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">Newest first</p>
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
                <span className="truncate text-(length:--fs-sm) text-(--fg2)">
                  {new Date(entry.revision.created_at).toLocaleString()} · {entry.machine}
                </span>
              </button>
              {position === 0 && <span className="text-(length:--fs-sm) text-(--fg3)">Latest</span>}
              <span className="text-(length:--fs-sm) text-(--fg3)">{plural((entry.revision.package_ids ?? []).length, "package")}</span>
              <Hash value={entry.revision.id} />
            </li>
          ))}
        </ul>
      </Section>
      <Section title={rev.label || "Saved revision"}>
        <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">{rev.mode || "default"} mode</p>
        <Button type="button" size="sm" className="mb-(--s2)" onClick={() => void navigate({ search: (prev) => ({ ...prev, restore: rev.id }) })}>
          Restore…
        </Button>
        {!previous ? (
          <p className="text-(length:--fs-sm)">First saved revision.</p>
        ) : changes.length === 0 ? (
          <p className="text-(length:--fs-sm)">Same packages as previous revision</p>
        ) : (
          <ul aria-label="Changes from previous revision" className="mb-(--s3) flex flex-col gap-(--s1) text-(length:--fs-sm)">
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
