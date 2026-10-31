import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Empty, Spinner } from "@/components/kit/feedback"
import { FieldError } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import type { Package, Revision } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { hud } from "@/lib/hud"
import { destinationsOf, expectedPins, restorePlan, type InstallItem, type RestorePlan } from "./install-state"
import { errorText, restorePreview, restoreRevision, saveDraft } from "./mutations"
import { draftsQuery } from "./queries"

type Row = { key: string; machine: string; directory: string; folderId: string; plan: RestorePlan; name: string }

const signs = { add: "+", update: "~", remove: "−" } as const
const actions = { add: "Add", update: "Update", remove: "Remove" } as const

// Opened from ?restore=<revision id> on an agentpack. One destination is chosen at a time.
export function RestoreSheet({
  packId,
  packName,
  revision,
  packages,
  open,
  onOpenChange,
}: {
  packId: string
  packName: string
  revision: Revision
  packages: Package[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="max-h-[85vh] overflow-y-auto">
        <RestoreForm packId={packId} packName={packName} revision={revision} packages={packages} onDone={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}

function RestoreForm({
  packId,
  packName,
  revision,
  packages,
  onDone,
}: {
  packId: string
  packName: string
  revision: Revision
  packages: Package[]
  onDone: () => void
}) {
  const queryClient = useQueryClient()
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const [chosen, setChosen] = useState<string>()
  const [restart, setRestart] = useState(true)
  const items: InstallItem[] = packages.map((pkg) => ({
    module: pkg.module,
    commit: pkg.commit,
    name: pkg.name || pkg.module,
    cache_key: pkg.cache_key,
    content_hash: pkg.content_hash,
  }))

  const rows: Row[] = destinationsOf(machines, projects.byMachine).map((target) => ({
    key: `${target.machine}\0${target.project.directory}`,
    machine: target.machine,
    directory: target.project.directory,
    folderId: target.project.folder_id,
    name: `${target.machineName} · ${basename(target.project.directory)}`,
    plan: restorePlan(target, items),
  }))
  const selected = rows.find((row) => row.key === chosen)
  const previewable = selected?.plan.kind === "restore" ? selected : undefined

  // The live diff for the chosen destination only. It is read fresh, never from a cache of an earlier choice.
  const preview = useQuery({
    queryKey: ["m", previewable?.machine ?? "", "library", "restore-preview", packId, revision.id, previewable?.directory ?? ""],
    queryFn: () => {
      if (!previewable) throw new Error("Choose a project to preview.")
      return restorePreview(previewable.machine, packId, revision.id, previewable.directory)
    },
    enabled: previewable !== undefined,
  })

  const apply = useMutation({
    mutationFn: async (row: Row) => {
      if (row.plan.kind === "draft") {
        await saveDraft({
          item: packId,
          pack_id: packId,
          revision: revision.id,
          node: row.machine,
          directory: row.directory,
          reason: row.plan.reason,
          folder_identity: row.folderId,
          expected_pins: expectedPins(items),
        })
        return "draft" as const
      }
      await restoreRevision(row.machine, packId, revision.id, row.directory, restart)
      return "restored" as const
    },
    onSuccess: async (outcome, row) => {
      if (outcome === "draft") {
        await queryClient.invalidateQueries({ queryKey: draftsQuery.queryKey })
        hud.success("Draft saved. It will be re-checked before anything is applied.")
      } else {
        await queryClient.invalidateQueries({ queryKey: projectsQuery(row.machine).queryKey })
        hud.success(`Restored ${packName} in ${basename(row.directory)}`)
      }
      onDone()
    },
  })

  const draft = selected?.plan.kind === "draft"
  const diff = preview.data
  let note: string | undefined
  if (!selected) note = "Choose an available project to preview the changes."
  else if (draft) note = "This project cannot be checked now. Save a draft, then review the exact diff when it is reachable."
  else if (selected.plan.kind === "blocked") note = selected.plan.text

  return (
    <>
      <SheetTitle>{`Restore “${packName}”`}</SheetTitle>
      <SheetDescription>
        {`Revision ${revision.label || "Saved revision"} · ${new Date(revision.created_at).toLocaleString()}. Select one trusted project and review its current pins.`}
      </SheetDescription>
      {rows.length === 0 && !projects.firstLoadPending ? (
        <Empty title="No project to restore into">Projects appear here after an agent runs in them.</Empty>
      ) : (
        <div role="radiogroup" aria-label="Destinations" className="flex flex-col gap-(--s2)">
          {rows.map((row) => (
            <label key={row.key} className="flex min-w-0 items-center gap-(--s3) text-(--fs)">
              <input
                type="radio"
                name="restore-target"
                className="size-4 shrink-0 accent-(--accent)"
                checked={chosen === row.key}
                disabled={row.plan.kind === "blocked" || apply.isPending}
                onChange={() => setChosen(row.key)}
              />
              <span className="min-w-0 flex-1 truncate">{row.name}</span>
              <span className="shrink-0 text-(--fg2)">{row.plan.kind === "restore" ? "Available" : row.plan.text}</span>
            </label>
          ))}
        </div>
      )}
      <section aria-label="Preview" className="flex flex-col gap-(--s2) text-(--fs-sm)">
        <div className="flex items-center justify-between text-(--fg2)">
          <span>Preview</span>
          <span>{`${revision.mode || "default"} mode`}</span>
        </div>
        {note && <p className="text-(--fg2)">{note}</p>}
        {previewable && preview.isPending && (
          <div className="flex items-center gap-(--s2) text-(--fg2)">
            <Spinner />
            Checking…
          </div>
        )}
        {previewable && preview.error && <FieldError>{`Couldn't preview this restore: ${errorText(preview.error)}`}</FieldError>}
        {previewable && diff && (
          <>
            {diff.add.length + diff.update.length + diff.remove.length === 0 ? (
              <p>No package changes</p>
            ) : (
              <ul aria-label="Package changes" className="flex flex-col gap-(--s1)">
                {(["add", "update", "remove"] as const).flatMap((kind) =>
                  diff[kind].map((module) => (
                    <li key={`${kind}\0${module}`}>
                      <span aria-hidden className="inline-block w-4">
                        {signs[kind]}
                      </span>
                      {`${actions[kind]} ${module}`}
                    </li>
                  )),
                )}
              </ul>
            )}
            <p className="text-(--fg2)">{`Restore mode ${diff.mode || revision.mode || "default"}; manifest and lock are backed up before apply.`}</p>
          </>
        )}
      </section>
      {!draft && (
        <label className="flex items-center gap-(--s3) text-(--fs-sm)">
          <Checkbox checked={restart} disabled={apply.isPending} onCheckedChange={setRestart} />
          Restart affected agents after restore
        </label>
      )}
      {apply.error && <FieldError>{`Couldn't restore ${packName}: ${errorText(apply.error)}`}</FieldError>}
      <SheetActions>
        <Button
          type="button"
          variant="primary"
          disabled={!selected || selected.plan.kind === "blocked" || (!draft && !diff) || apply.isPending}
          onClick={() => selected && apply.mutate(selected)}
        >
          {apply.isPending ? (draft ? "Saving draft…" : "Restoring…") : draft ? "Save draft" : "Restore"}
        </Button>
      </SheetActions>
    </>
  )
}
