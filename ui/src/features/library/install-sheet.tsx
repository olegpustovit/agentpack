import { useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Banner, Empty, Spinner } from "@/components/kit/feedback"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery, sessionsQuery } from "@/features/sessions/queries"
import { basename, shortHash } from "@/lib/format"
import { draftsQuery } from "./queries"
import { affectedAgents, destinationsOf, destinationState, expectedPins, installLabel, restartCopy, runLimited, type Destination, type InstallItem, type InstallPhase } from "./install-state"
import { errorText, installPackage, saveDraft } from "./mutations"

type Row = { key: string; target: Destination; disabled: boolean; draftReason: string; text: string }
type Status = { text: string; failed: boolean }

// Opened from ?install=1 on a package. The form mounts only while open, so each open starts clean.
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
  const sessions = useMachineQueries(sessionsQuery)
  const [picked, setPicked] = useState<string[]>([])
  const [restart, setRestart] = useState(true)
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
  const loadFailures = Object.entries(projects.errors).map(([id, error]) => `${machines.find((machine) => machine.id === id)?.name ?? id}: ${error.message}`)

  function setRowStatus(key: string, next: Status) {
    setStatus((current) => ({ ...current, [key]: next }))
  }

  async function run() {
    const jobs = chosen
    const spec = `${item.module}@${item.commit}`
    const installed = new Set<string>()
    let drafted = false
    setPhase("run")
    await runLimited(
      jobs,
      (job) => (job.draftReason ? "drafts" : job.target.machine),
      2,
      async (job) => {
        try {
          if (job.draftReason) {
            setRowStatus(job.key, { text: "Saving draft…", failed: false })
            await saveDraft({
              item: spec,
              node: job.target.machine,
              directory: job.target.project.directory,
              reason: job.draftReason,
              folder_identity: job.target.project.folder_id,
              expected_pins: expectedPins([item]),
            })
            drafted = true
            setRowStatus(job.key, { text: "Draft saved", failed: false })
          } else {
            setRowStatus(job.key, { text: "Installing…", failed: false })
            await installPackage(job.target.machine, job.target.project.directory, spec, restart)
            installed.add(job.target.machine)
            setRowStatus(job.key, { text: "Installed", failed: false })
          }
        } catch (error) {
          setRowStatus(job.key, { text: `Couldn't ${job.draftReason ? "save draft" : "install"}: ${errorText(error)}`, failed: true })
        }
      },
    )
    for (const machine of installed) await queryClient.invalidateQueries({ queryKey: projectsQuery(machine).queryKey })
    if (drafted) await queryClient.invalidateQueries({ queryKey: draftsQuery.queryKey })
    setPhase("done")
  }

  const destinationsByMachine = machines.map((machine) => ({ machine, rows: rows.filter((row) => row.target.machine === machine.id) })).filter((group) => group.rows.length > 0)
  const busy = phase !== "pick"

  return (
    <>
      <SheetTitle>{`Install “${item.name}”`}</SheetTitle>
      <SheetDescription>{`Pinned to ${item.module}@${shortHash(item.commit)}. Choose every project to install into. Nothing changes until you confirm.`}</SheetDescription>
      {loadFailures.map((failure) => (
        <Banner key={failure} tone="danger">{`Couldn't load projects from ${failure}`}</Banner>
      ))}
      {projects.firstLoadPending ? (
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
                const current = status[row.key]
                const checked = picked.includes(row.key)
                return (
                  <label key={row.key} className="flex min-w-0 items-center gap-(--s3) text-(length:--fs)">
                    <Checkbox
                      checked={checked}
                      disabled={row.disabled || busy}
                      onCheckedChange={(next) => setPicked((current) => (next ? [...current, row.key] : current.filter((value) => value !== row.key)))}
                    />
                    <span className="min-w-0 flex-1 truncate" title={row.target.project.directory}>
                      {basename(row.target.project.directory)}
                    </span>
                    <span className={current?.failed ? "shrink-0 text-(--danger)" : "shrink-0 text-(--fg2)"}>{current?.text ?? row.text}</span>
                  </label>
                )
              })}
            </section>
          ))}
        </div>
      )}
      <label className="flex items-center gap-(--s3) text-(length:--fs-sm)">
        <Checkbox checked={restart} disabled={busy} onCheckedChange={setRestart} />
        Restart affected agents
        {restart && affected > 0 && <span className="text-(--fg2)">{restartCopy(affected)}</span>}
      </label>
      <SheetActions>
        {phase === "pick" && chosen.length === 0 && <span className="self-center text-(length:--fs-sm) text-(--fg2)">Choose at least one project</span>}
        {phase === "done" ? (
          <Button type="button" variant="primary" onClick={onDone}>
            Done
          </Button>
        ) : (
          <Button type="button" variant="primary" disabled={phase === "run" || chosen.length === 0} onClick={() => void run()}>
            {installLabel(phase, installs, drafts)}
          </Button>
        )}
      </SheetActions>
    </>
  )
}
