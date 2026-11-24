import { errorText } from "@/lib/api"
import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { Boxes } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Hash, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { Banner, Empty, Spinner } from "@/components/kit/feedback"
import { hud } from "@/lib/hud"
import { resolveConflict } from "./mutations"
import { conflictQuery } from "./queries"
import { plural, shortHash } from "@/lib/format"
import { cn } from "@/lib/utils"
import { agentpackRevisions, formatRevisionChange, revisionChanges, type RevisionEntry } from "./inspect"
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
function ConflictPanel({ machine, id, revisions }: { machine: string; id: string; revisions: RevisionEntry[] }) {
  const queryClient = useQueryClient()
  const conflict = useQuery(conflictQuery(machine, id))
  const [choices, setChoices] = useState<Record<string, string>>({})
  const resolve = useMutation({
    mutationFn: () => resolveConflict(machine, id, conflict.data?.heads ?? [], choices),
    onSuccess: () => {
      hud.success("Conflict resolved")
      void queryClient.invalidateQueries({ queryKey: ["m", machine, "library"] })
    },
    onError: (error) => hud.error(`Couldn't resolve the conflict: ${errorText(error)}`),
  })

  if (conflict.isPending) {
    return (
      <Section title="Conflict" className="col-span-full">
        <div className="flex items-center gap-(--s2) text-(length:--fs-sm) text-(--fg2)">
          <Spinner />
          Loading conflict details…
        </div>
      </Section>
    )
  }

  if (conflict.isError) {
    return (
      <Section title="Conflict" className="col-span-full">
        <Banner tone="danger" className="flex flex-wrap items-center justify-between gap-(--s3)">
          <span>{`Couldn't load conflict details: ${errorText(conflict.error)}`}</span>
          <Button type="button" size="sm" onClick={() => void conflict.refetch()}>
            Retry
          </Button>
        </Banner>
      </Section>
    )
  }

  const data = conflict.data
  if (!data?.fields.length) return null
  const complete = data.fields.every((field) => choices[field.path])
  const revisionById = new Map(revisions.map((entry) => [entry.revision.id, entry]))

  return (
    <Section title="Conflict" className="col-span-full">
      <Banner tone="warn">Two machines changed this saved configuration differently. Choose which value to keep; the other stays in the history.</Banner>
      <div className="mt-(--s3) flex flex-col gap-(--s4)">
        {data.fields.map((field, index) => (
          <div key={field.path} className="flex min-w-0 flex-col gap-(--s2) border-b-[0.5px] border-(--sep) pb-(--s3) last:border-b-0">
            <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-(--s2)">
              <span className="min-w-0 flex-1 break-words font-medium" title={field.name}>{field.name}</span>
              <span className="shrink-0 text-(length:--fs-xs) text-(--fg2)">
                {`${index + 1} of ${plural(data.fields.length, "conflict")}`}
              </span>
            </div>
            <div role="radiogroup" aria-label={field.name} className="flex flex-col gap-(--s2)">
              {data.heads.map((head) => {
                const entry = revisionById.get(head)
                const meta = entry
                  ? `${entry.machine} · ${entry.revision.label || shortHash(head)} · ${new Date(entry.revision.created_at).toLocaleString()}`
                  : shortHash(head)
                return (
                  <label
                    key={head}
                    className={cn(
                      "flex min-h-8 min-w-0 cursor-pointer items-start gap-(--s3) rounded-(--r-row) border border-transparent px-(--s2) py-(--s2) hover:bg-(--hover) max-[760px]:min-h-11",
                      choices[field.path] === head && "border-(--accent-fill) bg-(--accent-soft)",
                    )}
                  >
                    <input
                      type="radio"
                      name={`conflict-${field.path}`}
                      className="mt-0.5 size-4 shrink-0 accent-(--accent)"
                      checked={choices[field.path] === head}
                      onChange={() => setChoices({ ...choices, [field.path]: head })}
                    />
                    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <code className="break-all text-(length:--fs)">{field.values[head]}</code>
                      <span className="text-(length:--fs-xs) text-(--fg2)">{meta}</span>
                    </span>
                  </label>
                )
              })}
            </div>
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

// `revision` selects a specific revision. An unknown ?revision shows a warning instead of silently substituting Latest.
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
  if (revisions.length === 0) {
    return (
      <MissingItem loading={loading} failures={failures} title="Revision details are unavailable">
        {`${pack.name} has no saved revisions on any machine.`}
      </MissingItem>
    )
  }

  const requestedIndex = revision ? revisions.findIndex((entry) => entry.revision.id === revision) : 0
  const requestedMissing = Boolean(revision) && requestedIndex === -1
  const selected = requestedIndex >= 0 ? revisions[requestedIndex] : undefined
  const latestEntry = revisions[0]!
  const previous = requestedIndex >= 0 ? revisions[requestedIndex + 1] : undefined
  const changes = selected ? revisionChanges(selected, previous) : []

  return (
    <Inspector>
      <Failures failures={failures} />
      {pack.conflict && holders[0] && <ConflictPanel machine={holders[0]} id={id} revisions={revisions} />}
      <InspectorHead
        icon={<Boxes aria-hidden />}
        title={pack.name}
        subtitle={`${(selected ?? latestEntry).revision.project || "Unknown project"} · saved on ${(selected ?? latestEntry).machine} · ${plural(revisions.length, "revision")}`}
      >
        <Button type="button" variant="ghost" size="sm" onClick={() => void navigate({ search: (prev) => ({ ...prev, save: "1" }) })}>
          Save new revision…
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={() => void navigate({ search: (prev) => ({ ...prev, rename: "1" }) })}>
          Rename…
        </Button>
      </InspectorHead>
      {requestedMissing && (
        <Banner tone="warn" className="col-span-full flex flex-wrap items-center justify-between gap-(--s3)">
          <span>{`Requested revision ${revision} is unavailable. Choose a revision from history or view the latest.`}</span>
          <Button type="button" size="sm" onClick={() => onRevision(latestEntry.revision.id)}>
            Show latest
          </Button>
        </Banner>
      )}
      {selected && (
        <Section title="Details" className="col-span-full">
          <dl className="flex flex-col gap-(--s3)">
            <KeyValue label="Source">
              <code>{selected.revision.project || "—"}</code> on {selected.machine}
            </KeyValue>
            <KeyValue label="Revision">
              <Hash value={selected.revision.id} />
            </KeyValue>
            <KeyValue label="Configuration">{selected.revision.needs_local_configuration ? "Needs local configuration" : "Complete on this machine"}</KeyValue>
          </dl>
        </Section>
      )}
      <Section title="History">
        <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">Newest first</p>
        <ul aria-label="Revision history" className="flex flex-col">
          {revisions.map((entry, position) => {
            const rowChanges = revisionChanges(entry, revisions[position + 1])
            const active = selected?.revision.id === entry.revision.id
            return (
              <li key={entry.revision.id} className="flex items-start gap-(--s3) py-(--s1)">
                <button
                  type="button"
                  aria-pressed={active}
                  onClick={() => onRevision(entry.revision.id)}
                  className={cn(
                    "flex min-w-0 flex-1 flex-col rounded-(--r-row) px-(--s3) py-(--s2) text-left hover:bg-(--hover)",
                    active && "bg-(--accent-soft)",
                  )}
                >
                  <span className="truncate font-medium">{entry.revision.label || "Saved revision"}</span>
                  <span className="truncate text-(length:--fs-sm) text-(--fg2)">
                    {new Date(entry.revision.created_at).toLocaleString()} · {entry.machine}
                  </span>
                  {rowChanges.length > 0 && (
                    <span className="break-words text-(length:--fs-xs) text-(--fg2)">
                      {rowChanges.map(formatRevisionChange).join(" · ")}
                    </span>
                  )}
                </button>
                <span className="flex shrink-0 flex-col items-end gap-0.5 pt-(--s2) text-(length:--fs-xs) text-(--fg3)">
                  {position === 0 && <span>Latest</span>}
                  <span>{plural((entry.revision.package_ids ?? []).length, "package")}</span>
                  <Hash value={entry.revision.id} />
                </span>
              </li>
            )
          })}
        </ul>
      </Section>
      {selected && (
        <Section title={selected.revision.label || "Saved revision"}>
          <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">{selected.revision.mode || "default"} mode</p>
          <Button type="button" size="sm" className="mb-(--s2)" onClick={() => void navigate({ search: (prev) => ({ ...prev, restore: selected.revision.id }) })}>
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
                  {formatRevisionChange(change)}
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
      )}
      {selected && (
        <RestoreSheet
          packId={id}
          packName={pack.name}
          revision={selected.revision}
          packages={selected.packages}
          open={search.restore === selected.revision.id}
          onOpenChange={(open) => {
            if (!open) void navigate({ search: (prev) => ({ ...prev, restore: undefined }), replace: true })
          }}
        />
      )}
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
