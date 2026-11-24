import { errorText } from "@/lib/api"
import { useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useReplicatedQueries } from "@/features/machines/queries"
import { projectsQuery, sessionsQuery } from "@/features/sessions/queries"
import { plural, shortHash } from "@/lib/format"
import { BatchResults, type BatchRow, type BatchRowState } from "./batch-results"
import type { InstalledRow } from "./inspect"
import { affectedAgentLabels, affectedAgents, restartCopy, runLimited } from "./install-state"
import { installPackage } from "./mutations"

export type UpdateTarget = { row: InstalledRow; commit: string }
type Status = { state: BatchRowState; text: string }

// Update is a write to every listed project, so it gets the same review as Install: where, what changes, restart off.
export function UpdateSheet({ targets, onClose }: { targets: UpdateTarget[]; onClose: () => void }) {
  return (
    <Sheet open={targets.length > 0} onOpenChange={(open) => !open && onClose()}>
      <SheetContent>{targets.length > 0 && <UpdateForm targets={targets} onDone={onClose} />}</SheetContent>
    </Sheet>
  )
}

function UpdateForm({ targets, onDone }: { targets: UpdateTarget[]; onDone: () => void }) {
  const queryClient = useQueryClient()
  const sessions = useReplicatedQueries(sessionsQuery)
  const [restart, setRestart] = useState(false)
  const [running, setRunning] = useState(false)
  const [started, setStarted] = useState(false)
  const [status, setStatus] = useState<Record<string, Status>>({})

  const failed = targets.filter(({ row }) => status[row.key]?.state === "failed")
  const succeeded = targets.filter(({ row }) => status[row.key]?.state === "done").length
  const finished = started && !running && targets.every(({ row }) => status[row.key]?.state === "done")
  const affected = targets.reduce((sum, { row }) => sum + affectedAgents(sessions.byMachine[row.machineId]?.data ?? [], row.directory), 0)
  const agentNames = [...new Set(targets.flatMap(({ row }) => affectedAgentLabels(sessions.byMachine[row.machineId]?.data ?? [], row.directory)))]

  function mark(key: string, next: Status) {
    setStatus((current) => ({ ...current, [key]: next }))
  }

  async function run(jobs: UpdateTarget[]) {
    setStarted(true)
    setRunning(true)
    setStatus((current) => {
      const next = { ...current }
      for (const job of jobs) next[job.row.key] = { state: "pending", text: "Pending" }
      return next
    })
    const updatedMachines = new Set<string>()
    await runLimited(
      jobs,
      (job) => job.row.machineId,
      2,
      async (job) => {
        mark(job.row.key, { state: "running", text: "Updating…" })
        try {
          await installPackage(job.row.machineId, job.row.directory, `${job.row.module}@${job.commit}`, restart)
          updatedMachines.add(job.row.machineId)
          mark(job.row.key, { state: "done", text: "Updated" })
        } catch (error) {
          mark(job.row.key, { state: "failed", text: `Couldn't update: ${errorText(error)}` })
        }
      },
    )
    for (const machineId of updatedMachines) {
      await queryClient.invalidateQueries({ queryKey: projectsQuery(machineId).queryKey })
    }
    setRunning(false)
  }

  const batchRows: BatchRow[] = targets.map(({ row, commit }) => {
    const current = status[row.key]
    return {
      key: row.key,
      title: row.project,
      detail: `${row.machine} · ${row.directory}`,
      trailing: `${shortHash(row.commit)} → ${shortHash(commit)}`,
      state: current?.state ?? "pending",
      statusText: current?.text ?? "",
    }
  })

  const summary =
    started && !running
      ? [succeeded > 0 && `${succeeded} updated`, failed.length > 0 && `${failed.length} failed`].filter(Boolean).join(" · ")
      : undefined
  const affectedNote =
    affected > 0
      ? restart
        ? `${restartCopy(affected)}${agentNames.length ? ` (${agentNames.join(", ")})` : ""}`
        : `${plural(affected, "agent")}${agentNames.length ? ` (${agentNames.join(", ")})` : ""} keep the old version until restarted`
      : undefined

  return (
    <>
      <SheetTitle>{`Update ${plural(targets.length, "project")}`}</SheetTitle>
      <SheetDescription>Each project moves to the new pin. Nothing changes until you confirm.</SheetDescription>
      <BatchResults label="Projects to update" rows={batchRows} summary={summary} />
      <label className="flex flex-wrap items-center gap-(--s3) text-(length:--fs-sm)">
        <Checkbox checked={restart} disabled={running || started} onCheckedChange={setRestart} />
        <span>Restart affected agents</span>
        {affectedNote && <span className="text-(--fg2)">{affectedNote}</span>}
      </label>
      <SheetActions>
        {finished ? (
          <Button type="button" variant="primary" onClick={onDone}>
            Done
          </Button>
        ) : failed.length > 0 && !running ? (
          <>
            <Button type="button" variant="primary" onClick={() => void run(failed)}>
              {`Retry failed (${failed.length})`}
            </Button>
            <Button type="button" onClick={onDone}>
              Done
            </Button>
          </>
        ) : (
          <Button type="button" variant="primary" disabled={running} onClick={() => void run(targets)}>
            {running ? "Updating…" : `Update ${plural(targets.length, "project")}`}
          </Button>
        )}
      </SheetActions>
    </>
  )
}
