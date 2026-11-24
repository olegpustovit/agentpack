import { ApiError, errorText } from "@/lib/api"
import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Banner, Empty, Spinner } from "@/components/kit/feedback"
import { FieldError } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useMachineQueries, useMachines, useReplicatedQueries } from "@/features/machines/queries"
import { projectsQuery, sessionsQuery } from "@/features/sessions/queries"
import type { Package, ProjectPackage, Revision } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { hud } from "@/lib/hud"
import { affectedAgentLabels, destinationsOf, expectedPins, restorePlan, type InstallItem, type RestorePlan } from "./install-state"
import { restorePreview, restoreRevision, saveDraft } from "./mutations"
import { draftsQuery } from "./queries"

type Row = { key: string; machine: string; machineName: string; directory: string; folderId: string; currentPackages: ProjectPackage[]; plan: RestorePlan; name: string }

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
  const [restart, setRestart] = useState(false)
  const [changedSinceReview, setChangedSinceReview] = useState(false)
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
    machineName: target.machineName,
    directory: target.project.directory,
    folderId: target.project.folder_id,
    currentPackages: target.project.packages,
    name: basename(target.project.directory),
    plan: restorePlan(target, items),
  }))
  const selected = rows.find((row) => row.key === chosen)
  const previewable = selected?.plan.kind === "restore" ? selected : undefined
  const previewKey = ["m", previewable?.machine ?? "", "library", "restore-preview", packId, revision.id, previewable?.directory ?? ""]

  // The live diff for the chosen destination only. It is read fresh, never from a cache of an earlier choice.
  const preview = useQuery({
    queryKey: previewKey,
    queryFn: () => {
      if (!previewable) throw new Error("Choose a project to preview.")
      return restorePreview(previewable.machine, packId, revision.id, previewable.directory)
    },
    enabled: previewable !== undefined,
    staleTime: "static",
    gcTime: 0,
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
          expected_pins: expectedPins(row.currentPackages),
        })
        return "draft" as const
      }
      const reviewedDigest = preview.data?.preview_digest
      if (!reviewedDigest) throw new Error("Needs update")
      const { data: fresh, isError } = await preview.refetch()
      if (isError) return "changed" as const
      if (fresh?.preview_digest !== reviewedDigest) {
        setChangedSinceReview(true)
        return "changed" as const
      }
      try {
        await restoreRevision(row.machine, packId, revision.id, row.directory, restart, reviewedDigest)
        return "restored" as const
      } catch (error) {
        if (error instanceof ApiError && error.status === 409) {
          setChangedSinceReview(true)
          await preview.refetch()
          return "changed" as const
        }
        throw error
      }
    },
    onSuccess: async (outcome, row) => {
      if (outcome === "changed") return
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
      {changedSinceReview && (
        <Banner tone="warn">Changed since you reviewed. Check the updated changes below and confirm again.</Banner>
      )}
      {rows.length === 0 && !projects.firstLoadPending ? (
        <Empty title="No project to restore into">Projects appear here after an agent runs in them.</Empty>
      ) : (
        <div role="radiogroup" aria-label="Destinations" className="flex flex-col gap-(--s2)">
          {rows.map((row) => (
            <label key={row.key} className="flex min-h-6 min-w-0 items-center gap-(--s3) text-(length:--fs) max-[760px]:min-h-11">
              <input
                type="radio"
                name="restore-target"
                className="size-4 shrink-0 accent-(--accent)"
                checked={chosen === row.key}
                disabled={row.plan.kind === "blocked" || apply.isPending}
                onChange={() => {
                  setChosen(row.key)
                  setRestart(false)
                  setChangedSinceReview(false)
                }}
              />
              <span className="flex min-w-16 flex-1 flex-col" title={row.directory}>
                <span className="truncate">{row.name}</span>
                <span className="truncate text-(length:--fs-xs) text-(--fg2)">{`${row.machineName} · ${row.directory}`}</span>
              </span>
              <span className="shrink-0 text-(length:--fs-sm) text-(--fg2)">{row.plan.kind === "restore" ? "Available" : row.plan.text}</span>
            </label>
          ))}
        </div>
      )}
      <section aria-label="Preview" className="flex flex-col gap-(--s2) text-(length:--fs-sm)">
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
        {previewable && (preview.error || (diff && !diff.preview_digest)) && (
          <div>
            {preview.error && <FieldError>{`Couldn't preview this restore: ${errorText(preview.error)}`}</FieldError>}
            {!preview.error && <Banner tone="warn">Needs update. Update the destination gateway to confirm this restore.</Banner>}
            <Button type="button" disabled={preview.isFetching || apply.isPending} onClick={() => void preview.refetch()}>Review again</Button>
          </div>
        )}
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
      {previewable && (
        <RestoreRestartChoice
          machine={previewable.machine}
          directory={previewable.directory}
          restart={restart}
          disabled={apply.isPending}
          onChange={setRestart}
        />
      )}
      {apply.error && <FieldError>{`Couldn't restore ${packName}: ${errorText(apply.error)}`}</FieldError>}
      <SheetActions>
        <Button
          type="button"
          variant="primary"
          disabled={!selected || selected.plan.kind === "blocked" || (!draft && (!diff?.preview_digest || preview.isFetching || preview.isError)) || apply.isPending}
          onClick={() => selected && apply.mutate(selected)}
        >
          {apply.isPending ? (draft ? "Saving draft…" : "Restoring…") : draft ? "Save draft" : changedSinceReview ? "Confirm restore" : "Restore"}
        </Button>
      </SheetActions>
    </>
  )
}

export function RestoreRestartChoice({
  machine, directory, restart, disabled, onChange,
}: {
  machine: string
  directory: string
  restart: boolean
  disabled: boolean
  onChange: (restart: boolean) => void
}) {
  const sessions = useReplicatedQueries(sessionsQuery)
  const target = sessions.byMachine[machine]
  const agents = affectedAgentLabels(target?.data ?? [], directory)
  const checking = target?.pending ?? sessions.firstLoadPending
  return (
    <div className="flex min-w-0 flex-col gap-(--s2) text-(length:--fs-sm)">
      {agents.length > 0 && (
        <section aria-label="Affected sessions">
          <h3 className="font-medium">Affected sessions</h3>
          <ul className="list-inside list-disc break-words text-(--fg2)">
            {agents.map((agent, index) => <li key={`${agent}-${index}`}>{agent}</li>)}
          </ul>
        </section>
      )}
      {checking && <p className="text-(--fg2)">Checking affected sessions…</p>}
      {target?.error && <FieldError>{`Couldn't check affected sessions: ${errorText(target.error)}`}</FieldError>}
      <label className="flex min-h-6 items-center gap-(--s3) max-[760px]:min-h-11">
        <Checkbox checked={restart} disabled={disabled || checking || Boolean(target?.error)} onCheckedChange={onChange} />
        Restart affected agents after restore
      </label>
    </div>
  )
}
