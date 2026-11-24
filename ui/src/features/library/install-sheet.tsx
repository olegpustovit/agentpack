import { errorText } from "@/lib/api"
import { useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Banner, Empty, Spinner } from "@/components/kit/feedback"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useMachineQueries, useReplicatedQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery, sessionsQuery } from "@/features/sessions/queries"
import { basename, plural, shortHash } from "@/lib/format"
import { BatchResults, type BatchRow, type BatchRowState } from "./batch-results"
import { draftsQuery } from "./queries"
import {
  affectedAgentLabels,
  affectedAgents,
  destinationsOf,
  destinationState,
  expectedPins,
  installLabel,
  restartCopy,
  runLimited,
  type Destination,
  type InstallItem,
  type InstallPhase,
} from "./install-state"
import { installPackage, saveDraft } from "./mutations"

type Row = { key: string; target: Destination; disabled: boolean; draftReason: string; text: string }
type Status = { state: BatchRowState; text: string }

// Opened from ?install=1 on a package, or from Discover's "Install in another project".
export function InstallSheet({ item, open, onOpenChange }: { item: InstallItem; open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        <InstallForm item={item} onDone={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}

function InstallForm({ item, onDone }: { item: InstallItem; onDone: () => void }) {
  const queryClient = useQueryClient()
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const sessions = useReplicatedQueries(sessionsQuery)
  const [picked, setPicked] = useState<string[]>([])
  const [restart, setRestart] = useState(false)
  const [phase, setPhase] = useState<InstallPhase>("pick")
  const [status, setStatus] = useState<Record<string, Status>>({})

  const rows: Row[] = destinationsOf(machines, projects.byMachine).map((target) => ({
    key: `${target.machine}\0${target.project.directory}`,
    target,
    ...destinationState(item, target),
  }))
  const chosen = rows.filter((row) => picked.includes(row.key) && !row.disabled)
  const drafts = chosen.filter((row) => row.draftReason !== "").length
  const installs = chosen.length - drafts
  const affected = chosen.reduce((sum, row) => sum + affectedAgents(sessions.byMachine[row.target.machine]?.data ?? [], row.target.project.directory), 0)
  const agentNames = [...new Set(chosen.flatMap((row) => affectedAgentLabels(sessions.byMachine[row.target.machine]?.data ?? [], row.target.project.directory)))]
  const loadFailures = Object.entries(projects.errors).map(([id, error]) => `${machines.find((machine) => machine.id === id)?.name ?? id}: ${error.message}`)

  function setRowStatus(key: string, next: Status) {
    setStatus((current) => ({ ...current, [key]: next }))
  }

  async function run(jobs: Row[]) {
    const spec = `${item.module}@${item.commit}`
    const installed = new Set<string>()
    let drafted = false
    setPhase("run")
    setStatus((current) => {
      const next = { ...current }
      for (const job of jobs) next[job.key] = { state: "pending", text: "Pending" }
      return next
    })
    await runLimited(
      jobs,
      (job) => (job.draftReason ? "drafts" : job.target.machine),
      2,
      async (job) => {
        try {
          if (job.draftReason) {
            setRowStatus(job.key, { state: "running", text: "Saving draft…" })
            await saveDraft({
              item: spec,
              node: job.target.machine,
              directory: job.target.project.directory,
              reason: job.draftReason,
              folder_identity: job.target.project.folder_id,
              expected_pins: expectedPins(job.target.project.packages),
            })
            drafted = true
            setRowStatus(job.key, { state: "done", text: "Draft saved" })
          } else {
            setRowStatus(job.key, { state: "running", text: "Installing…" })
            await installPackage(job.target.machine, job.target.project.directory, spec, restart)
            installed.add(job.target.machine)
            setRowStatus(job.key, { state: "done", text: "Installed" })
          }
        } catch (error) {
          setRowStatus(job.key, { state: "failed", text: `Couldn't ${job.draftReason ? "save draft" : "install"}: ${errorText(error)}` })
        }
      },
    )
    for (const machine of installed) await queryClient.invalidateQueries({ queryKey: projectsQuery(machine).queryKey })
    if (drafted) await queryClient.invalidateQueries({ queryKey: draftsQuery.queryKey })
    setPhase("done")
  }

  const destinationsByMachine = machines.map((machine) => ({ machine, rows: rows.filter((row) => row.target.machine === machine.id) })).filter((group) => group.rows.length > 0)
  const busy = phase !== "pick"
  const failed = chosen.filter((row) => status[row.key]?.state === "failed")
  const succeeded = chosen.filter((row) => status[row.key]?.state === "done").length
  const batchRows: BatchRow[] = chosen.map((row) => {
    const current = status[row.key]
    return {
      key: row.key,
      title: basename(row.target.project.directory),
      detail: `${row.target.machineName} · ${row.target.project.directory}`,
      state: current?.state ?? "pending",
      statusText: current?.text ?? "Pending",
    }
  })
  const resultSummary =
    phase === "done"
      ? [succeeded > 0 && `${succeeded} completed`, failed.length > 0 && `${failed.length} failed`].filter(Boolean).join(" · ")
      : undefined
  const affectedNote =
    affected > 0
      ? restart
        ? `${restartCopy(affected)}${agentNames.length ? ` (${agentNames.join(", ")})` : ""}`
        : `${plural(affected, "agent")}${agentNames.length ? ` (${agentNames.join(", ")})` : ""} keep the old version until restarted`
      : undefined

  return (
    <>
      <SheetTitle>{`Install “${item.name}”`}</SheetTitle>
      <SheetDescription>{`Pinned to ${item.module}@${shortHash(item.commit)}. Choose every project to install into. Nothing changes until you confirm.`}</SheetDescription>
      {loadFailures.map((failure) => (
        <Banner key={failure} tone="danger">{`Couldn't load projects from ${failure}`}</Banner>
      ))}
      {phase !== "pick" ? (
        <BatchResults label="Install results" rows={batchRows} summary={resultSummary} />
      ) : projects.firstLoadPending ? (
        <div className="flex justify-center p-(--s5)">
          <Spinner />
        </div>
      ) : rows.length === 0 ? (
        <Empty title="No project to install into">Projects appear here after an agent runs in them.</Empty>
      ) : (
        <div className="flex max-h-96 flex-col gap-(--s4) overflow-y-auto" role="group" aria-label="Destinations">
          {destinationsByMachine.map(({ machine, rows: group }) => (
            <section key={machine.id} className="flex flex-col gap-(--s2)">
              <h3 className="text-(length:--fs-sm) font-medium text-(--fg2)">{`${machine.name} · ${machine.online ? "Online" : "Offline"}`}</h3>
              {group.map((row) => {
                const checked = picked.includes(row.key)
                return (
                  <label key={row.key} className="flex min-h-6 min-w-0 items-center gap-(--s3) text-(length:--fs) max-[760px]:min-h-11">
                    <Checkbox
                      checked={checked}
                      disabled={row.disabled || busy}
                      onCheckedChange={(next) => setPicked((current) => (next ? [...current, row.key] : current.filter((value) => value !== row.key)))}
                    />
                    {/* The parent path tells two projects with one name apart, for sighted and screen-reader users alike. */}
                    <span className="flex min-w-16 flex-1 flex-col" title={row.target.project.directory}>
                      <span className="truncate">{basename(row.target.project.directory)}</span>
                      <span className="truncate text-(length:--fs-xs) text-(--fg2)">{`${machine.name} · ${row.target.project.directory}`}</span>
                    </span>
                    <span className="max-w-[65%] shrink-0 text-(length:--fs-sm) text-(--fg2)" title={row.text}>{row.text}</span>
                  </label>
                )
              })}
            </section>
          ))}
        </div>
      )}
      {phase === "pick" && (
        <label className="flex flex-wrap items-center gap-(--s3) text-(length:--fs-sm)">
          <Checkbox checked={restart} disabled={busy} onCheckedChange={setRestart} />
          <span>Restart affected agents</span>
          {affectedNote && <span className="text-(--fg2)">{affectedNote}</span>}
        </label>
      )}
      <SheetActions
        status={phase === "pick" ? (chosen.length === 0 ? "Choose at least one project" : `${plural(chosen.length, "project")} selected`) : undefined}
      >
        {phase === "done" ? (
          <>
            {failed.length > 0 && (
              <Button type="button" onClick={() => void run(failed)}>
                {`Retry failed (${failed.length})`}
              </Button>
            )}
            <Button type="button" variant="primary" onClick={onDone}>
              Done
            </Button>
          </>
        ) : (
          <Button type="button" variant="primary" disabled={phase === "run" || chosen.length === 0} onClick={() => void run(chosen)}>
            {installLabel(phase, installs, drafts)}
          </Button>
        )}
      </SheetActions>
    </>
  )
}
