import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Section } from "@/components/kit/data"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { worktreeState, worktreesQuery, type WorktreeRow } from "@/features/sessions/git"
import { api, routeMissing } from "@/lib/api"
import { basename, plural } from "@/lib/format"
import { hud } from "@/lib/hud"

function size(bytes: number): string {
  const mb = bytes / 1024 ** 2
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.max(1, Math.round(mb))} MB`
}

// Worktrees agentpack made on this machine that are still there, with what is left in each.
export function WorktreesSection({ machine, online }: { machine: string; online: boolean }) {
  const queryClient = useQueryClient()
  const worktrees = useQuery({ ...worktreesQuery(machine), enabled: online })
  const [removing, setRemoving] = useState<WorktreeRow | null>(null)
  const refresh = () => void queryClient.invalidateQueries({ queryKey: worktreesQuery(machine).queryKey })
  const remove = useMutation({
    mutationFn: ({ row, discard }: { row: WorktreeRow; discard: boolean }) => api("/api/worktrees/remove", { machine, method: "POST", body: { path: row.path, discard } }),
    onSuccess: () => {
      hud.success("Worktree removed")
      refresh()
    },
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
      ) : (
        <DataGrid aria-label="Worktrees">
          <DataGridHead>
            <DataGridRow>
              <DataGridHeaderCell className="w-56">Branch</DataGridHeaderCell>
              <DataGridHeaderCell className="w-36">Project</DataGridHeaderCell>
              <DataGridHeaderCell>State</DataGridHeaderCell>
              <DataGridHeaderCell className="w-24 text-right">Sessions</DataGridHeaderCell>
              <DataGridHeaderCell className="w-20 text-right">Size</DataGridHeaderCell>
              <th className="w-44 border-b-[0.5px] border-(--sep)"><span className="sr-only">Actions</span></th>
            </DataGridRow>
          </DataGridHead>
          <tbody>
            {rows.map((row) => (
              <DataGridRow key={row.path}>
                <DataGridCell title={row.path}>{row.branch || basename(row.path)}</DataGridCell>
                <DataGridCell className="text-(--fg2)" title={row.main}>{row.main ? basename(row.main) : "—"}</DataGridCell>
                <DataGridCell>{worktreeState(row)}</DataGridCell>
                <DataGridCell className="text-right tabular-nums">{row.running ? `${row.running} running` : row.sessions.length || "—"}</DataGridCell>
                <DataGridCell className="text-right tabular-nums text-(--fg2)">{size(row.size)}</DataGridCell>
                <DataGridCell>
                  <div className="flex justify-end gap-(--s2)">
                  {row.unpushed > 0 && !row.merged && !row.orphaned && (
                    <Button type="button" size="sm" disabled={!online || push.isPending} onClick={() => push.mutate(row)}>
                      Push
                    </Button>
                  )}
                  <Button type="button" size="sm" disabled={!online || row.running > 0 || remove.isPending} title={row.running > 0 ? "Stop its sessions first" : undefined} onClick={() => setRemoving(row)}>
                    Remove…
                  </Button>
                  </div>
                </DataGridCell>
              </DataGridRow>
            ))}
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
                  ? `Removes the worktree and loses ${plural(changed, "file")} that were never committed. The branch stays.`
                  : "Removes the worktree folder. The branch and its commits stay."
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
