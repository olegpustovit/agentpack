import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Section } from "@/components/kit/data"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { worktreeState, worktreesQuery, type WorktreeRow } from "@/features/sessions/git"
import { api, routeMissing } from "@/lib/api"
import { basename, plural } from "@/lib/format"
import { hud } from "@/lib/hud"

function size(bytes: number): string {
  const mb = bytes / 1024 ** 2
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.max(1, Math.round(mb))} MB`
}

function removeDisabledReason(row: WorktreeRow, online: boolean): string | undefined {
  if (!online) return "Machine offline"
  if (row.running > 0) return `Stop ${plural(row.running, "running session")} first`
  return undefined
}

// Worktrees agentpack made on this machine that are still there, with what is left in each.
export function WorktreesSection({ machine, online }: { machine: string; online: boolean }) {
  const isPhone = useIsPhone()
  const queryClient = useQueryClient()
  const worktrees = useQuery({ ...worktreesQuery(machine), enabled: online })
  const [removing, setRemoving] = useState<WorktreeRow | null>(null)
  const refresh = () => void queryClient.invalidateQueries({ queryKey: worktreesQuery(machine).queryKey })
  const remove = useMutation({
    mutationFn: ({ row, discard }: { row: WorktreeRow; discard: boolean }) => api("/api/worktrees/remove", { machine, method: "POST", body: { path: row.path, discard } }),
    onSuccess: refresh,
    onError: (error) => hud.error(`Couldn't remove the worktree: ${error.message}`),
  })
  const push = useMutation({
    mutationFn: (row: WorktreeRow) => api("/api/worktrees/push", { machine, method: "POST", body: { path: row.path } }),
    onSuccess: (_, row) => {
      hud.success(`Pushed ${row.branch}`)
      refresh()
    },
    onError: (error, row) => hud.error(`Couldn't push ${row.branch}: ${error.message}`, () => push.mutate(row)),
  })
  const needsUpdate = routeMissing(worktrees.error)
  const rows = worktrees.data?.worktrees ?? []
  if (!needsUpdate && rows.length === 0) return null
  const changed = removing ? removing.dirty + removing.untracked : 0
  return (
    <Section title={rows.length ? `Worktrees · ${rows.length}` : "Worktrees"} className="col-span-full">
      {needsUpdate ? (
        <p className="text-(--fg2)">Needs update</p>
      ) : isPhone ? (
        <ul aria-label="Worktrees" className="flex flex-col divide-y-[0.5px] divide-(--sep)">
          {rows.map((row) => {
            const disabledReason = removeDisabledReason(row, online)
            return (
              <li key={row.path} className="flex min-w-0 flex-col gap-(--s2) py-(--s3)">
                <div className="flex min-w-0 items-baseline justify-between gap-(--s2)">
                  <span className="min-w-0 truncate font-medium" title={row.path}>{row.branch || basename(row.path)}</span>
                  <span className="shrink-0 text-(length:--fs-xs) text-(--fg2) tabular-nums">{size(row.size)}</span>
                </div>
                <p className="text-(length:--fs-sm) text-(--fg2)">
                  {[row.main ? basename(row.main) : undefined, worktreeState(row), row.running ? `${row.running} running` : undefined].filter(Boolean).join(" · ")}
                </p>
                <div className="flex flex-wrap items-center gap-(--s2)">
                  {row.unpushed > 0 && !row.merged && !row.orphaned && (
                    <Button type="button" size="sm" disabled={!online || push.isPending} onClick={() => push.mutate(row)}>
                      Push
                    </Button>
                  )}
                  <Button type="button" size="sm" disabled={Boolean(disabledReason) || remove.isPending} onClick={() => setRemoving(row)}>
                    Remove…
                  </Button>
                  {disabledReason && <span className="text-(length:--fs-xs) text-(--fg2)">{disabledReason}</span>}
                </div>
              </li>
            )
          })}
        </ul>
      ) : (
        <DataGrid aria-label="Worktrees">
          <DataGridHead>
            <DataGridRow>
              <DataGridHeaderCell className="w-56">Branch</DataGridHeaderCell>
              <DataGridHeaderCell className="w-36">Project</DataGridHeaderCell>
              <DataGridHeaderCell>State</DataGridHeaderCell>
              <DataGridHeaderCell className="w-24 text-right">Sessions</DataGridHeaderCell>
              <DataGridHeaderCell className="w-20 text-right">Size</DataGridHeaderCell>
              <th className="w-52 border-b-[0.5px] border-(--sep)"><span className="sr-only">Actions</span></th>
            </DataGridRow>
          </DataGridHead>
          <tbody>
            {rows.map((row) => {
              const disabledReason = removeDisabledReason(row, online)
              return (
                <DataGridRow key={row.path}>
                  <DataGridCell title={row.path}>{row.branch || basename(row.path)}</DataGridCell>
                  <DataGridCell className="text-(--fg2)" title={row.main}>{row.main ? basename(row.main) : "—"}</DataGridCell>
                  <DataGridCell>{worktreeState(row)}</DataGridCell>
                  <DataGridCell className="text-right tabular-nums">{row.running ? `${row.running} running` : row.sessions.length || "—"}</DataGridCell>
                  <DataGridCell className="text-right tabular-nums text-(--fg2)">{size(row.size)}</DataGridCell>
                  <DataGridCell>
                    <div className="flex flex-wrap items-center justify-end gap-(--s2)">
                      {row.unpushed > 0 && !row.merged && !row.orphaned && (
                        <Button type="button" size="sm" disabled={!online || push.isPending} onClick={() => push.mutate(row)}>
                          Push
                        </Button>
                      )}
                      <Button type="button" size="sm" disabled={Boolean(disabledReason) || remove.isPending} onClick={() => setRemoving(row)}>
                        Remove…
                      </Button>
                      {disabledReason && <span className="text-(length:--fs-xs) text-(--fg2)">{disabledReason}</span>}
                    </div>
                  </DataGridCell>
                </DataGridRow>
              )
            })}
          </tbody>
        </DataGrid>
      )}
      <ConfirmAlert open={removing !== null} onOpenChange={(open) => { if (!open) setRemoving(null) }}>
        {removing && (
          <ConfirmAlertContent
            title={changed > 0 ? `Discard ${plural(changed, "uncommitted file")}?` : `Remove ${removing.branch || basename(removing.path)}?`}
            description={
              removing.orphaned
                ? "Its main folder is gone. Deletes the leftover folder."
                : changed > 0
                  ? `Removes the worktree for ${removing.branch || basename(removing.path)} and loses ${plural(changed, "file")} that were never committed${removing.unpushed > 0 ? ` (${plural(removing.unpushed, "unpushed commit")} remain on the branch)` : ""}. The branch stays.`
                  : `Removes the worktree folder for ${removing.branch || basename(removing.path)}. The branch and its commits stay.`
            }
            destructiveLabel={changed > 0 ? "Discard and remove" : "Remove"}
            onConfirm={() => {
              const row = removing
              setRemoving(null)
              remove.mutate({ row, discard: changed > 0 })
            }}
          />
        )}
      </ConfirmAlert>
    </Section>
  )
}
