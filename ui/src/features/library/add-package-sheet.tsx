import { useState, type FormEvent } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Empty } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import { errorText } from "@/lib/api"
import type { MCPServerInput } from "@/lib/api-types"
import { basename, plural } from "@/lib/format"
import { hud } from "@/lib/hud"
import { BatchResults, type BatchRow, type BatchRowState } from "./batch-results"
import { destinationState } from "./install-state"
import type { PackageItem } from "./model"
import { installMCP, installPackage } from "./mutations"
import { SavePackForm } from "./save-sheet"
import { useLibrary } from "./use-library"

// Opened from ?add=1 (or ?add=<spec> from Discover) on the Library, or from a project row with that project preselected.
export function AddPackageSheet({
  open,
  initialMachine,
  initialDirectory,
  initialSpec,
  onOpenChange,
}: {
  open: boolean
  initialMachine?: string
  initialDirectory?: string
  initialSpec?: string
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        <AddPackageForm onDone={() => onOpenChange(false)} initialMachine={initialMachine} initialDirectory={initialDirectory} initialSpec={initialSpec} />
      </SheetContent>
    </Sheet>
  )
}

// Save agentpack from the Library toolbar after choosing a project.
export function SaveAgentpackSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const [choice, setChoice] = useState<string>()
  const eligible = machines.filter((machine) => machine.online).flatMap((machine) =>
    (projects.byMachine[machine.id]?.data ?? [])
      .filter((project) => project.available)
      .map((project) => ({
        value: `${machine.id}\0${project.directory}`,
        label: `${machine.name} · ${project.name || basename(project.directory)} (${project.directory})`,
        machine,
        project,
      })),
  )
  const current = eligible.find((entry) => entry.value === choice) ?? eligible[0]
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        {current ? (
          <SavePackForm
            key={current.value}
            machine={current.machine.id}
            machineName={current.machine.name}
            directory={current.project.directory}
            mode={current.project.launches[0]?.mode ?? "default"}
            onDone={() => onOpenChange(false)}
          >
            <Field>
              <FieldLabel>Project</FieldLabel>
              <PopupSelect label="Project" value={current.value} items={eligible} onValueChange={setChoice} />
            </Field>
          </SavePackForm>
        ) : (
          <>
            <SheetTitle>Save agentpack</SheetTitle>
            <Empty title="No project to save">Projects appear here after an agent runs in them on an online machine.</Empty>
            <SheetActions>{null}</SheetActions>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

// Install an MCP server from Discover or the MCP inspector into a project on an online machine.
export function InstallMCPSheet({ server, onOpenChange }: { server?: MCPServerInput; onOpenChange: (open: boolean) => void }) {
  return (
    <Sheet open={Boolean(server)} onOpenChange={onOpenChange}>
      <SheetContent>{server && <InstallMCPForm server={server} onDone={() => onOpenChange(false)} />}</SheetContent>
    </Sheet>
  )
}

function InstallMCPForm({ server, onDone }: { server: MCPServerInput; onDone: () => void }) {
  const queryClient = useQueryClient()
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const [choice, setChoice] = useState<string>()
  const [restart, setRestart] = useState(false)
  const eligible = machines.filter((machine) => machine.online).flatMap((machine) =>
    (projects.byMachine[machine.id]?.data ?? [])
      .filter((project) => project.available)
      .map((project) => {
        const installed = project.mcp.some((entry) => entry.name === server.name)
        const title = project.name || basename(project.directory)
        return {
          value: `${machine.id}\0${project.directory}`,
          label: `${machine.name} · ${title}${installed ? " · Already installed" : ""}`,
          machine: machine.id,
          machineName: machine.name,
          installed,
          project,
        }
      }),
  )
  const current = eligible.find((entry) => entry.value === choice) ?? eligible.find((entry) => !entry.installed) ?? eligible[0]
  const alreadyInstalled = eligible.filter((entry) => entry.installed)
  const install = useMutation({
    mutationFn: () => {
      if (!current) throw new Error("Choose a project to install into.")
      return installMCP(current.machine, current.project.directory, server, restart)
    },
    onSuccess: async () => {
      if (current) await queryClient.invalidateQueries({ queryKey: projectsQuery(current.machine).queryKey })
      hud.success(`Added ${server.name}`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    install.mutate()
  }

  const failure = install.error && errorText(install.error)
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>{`Install ${server.name}`}</SheetTitle>
      <SheetDescription>{server.url ? server.url : [server.command, ...(server.args ?? [])].join(" ")}</SheetDescription>
      {!current ? (
        <Empty title="No project to install into">Projects appear here after an agent runs in them on an online machine.</Empty>
      ) : (
        <FieldGrid>
          <Field>
            <FieldLabel>Project</FieldLabel>
            <PopupSelect label="Project" value={current.value} items={eligible} onValueChange={setChoice} />
          </Field>
          <Field>
            <span aria-hidden className="w-30 shrink-0" />
            <span className="min-w-0 flex-1 break-all text-(length:--fs-xs) text-(--fg2)">
              {`${current.machineName} · ${current.project.directory}${current.installed ? " · Already installed" : ""}`}
            </span>
          </Field>
          {alreadyInstalled.length > 0 && !current.installed && (
            <Field>
              <span aria-hidden className="w-30 shrink-0" />
              <span className="min-w-0 flex-1 text-(length:--fs-xs) text-(--fg2)">
                {`Already installed in: ${alreadyInstalled.map((entry) => `${entry.machineName} · ${entry.project.name || basename(entry.project.directory)}`).join(", ")}`}
              </span>
            </Field>
          )}
          <Field>
            <span aria-hidden className="w-30 shrink-0" />
            <label className="flex min-h-6 items-center gap-(--s3) text-(length:--fs-sm) max-[760px]:min-h-11">
              <Checkbox checked={restart} onCheckedChange={setRestart} />
              Restart affected agents now
            </label>
          </Field>
        </FieldGrid>
      )}
      {failure && <FieldError>{`Couldn't install ${server.name}: ${failure}`}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!current || install.isPending}>
          {install.isPending ? "Installing…" : "Install"}
        </Button>
      </SheetActions>
    </form>
  )
}

type AddJob = { key: string; spec: string }
type JobStatus = { state: BatchRowState; text: string }

function AddPackageForm({ onDone, initialMachine, initialDirectory, initialSpec }: { onDone: () => void; initialMachine?: string; initialDirectory?: string; initialSpec?: string }) {
  const queryClient = useQueryClient()
  const machines = useMachines()
  const { items } = useLibrary()
  const projects = useMachineQueries(projectsQuery)
  const [spec, setSpec] = useState(initialSpec ?? "")
  const [picked, setPicked] = useState<Record<string, boolean>>({})
  const [restart, setRestart] = useState(false)
  const [choice, setChoice] = useState<string | undefined>(initialMachine && initialDirectory ? `${initialMachine}\0${initialDirectory}` : undefined)
  const [blank, setBlank] = useState(false)
  const [running, setRunning] = useState(false)
  const [status, setStatus] = useState<Record<string, JobStatus>>({})
  const fixed = Boolean(initialDirectory)

  const initialModule = initialSpec ? initialSpec.split("@")[0]!.toLowerCase() : ""
  const eligible = machines
    .filter((machine) => machine.online)
    .flatMap((machine) =>
      (projects.byMachine[machine.id]?.data ?? [])
        .filter((project) => project.available)
        .map((project) => {
          const already = Boolean(initialModule) && project.packages.some((entry) => entry.module.toLowerCase() === initialModule)
          return {
            value: `${machine.id}\0${project.directory}`,
            machine: machine.id,
            directory: project.directory,
            label: `${machine.name} · ${project.name || basename(project.directory)} (${project.directory})${already ? " · Already installed" : ""}`,
            target: { machine: machine.id, machineName: machine.name, online: machine.online, project },
          }
        }),
    )
  const listed = eligible.find((entry) => entry.value === choice)
  // A session's folder may not be a recorded project yet. It must still install there, never into the first project listed.
  const unlisted = fixed && !listed && initialMachine && initialDirectory
    ? {
        value: `${initialMachine}\0${initialDirectory}`,
        machine: initialMachine,
        directory: initialDirectory,
        label: basename(initialDirectory),
        target: {
          machine: initialMachine,
          machineName: initialMachine,
          online: true,
          project: { directory: initialDirectory, name: basename(initialDirectory), folder_id: "", available: true, unavailable_reason: "", last_used: "", launches: [], packages: [], mcp: [], modes: [], error: "" },
        },
      }
    : undefined
  const current = listed ?? unlisted ?? (choice === undefined ? eligible[0] : undefined)
  const [batch, setBatch] = useState<{ jobs: AddJob[]; destination: NonNullable<typeof current> }>()
  const jobs = batch?.jobs ?? []
  const hasResults = Object.keys(status).length > 0
  const known = items.filter((item): item is PackageItem => item.type === "skill" || item.type === "plugin")
  const chosenJobs: AddJob[] = known
    .filter((item) => picked[item.key])
    .map((item) => ({ key: item.key, spec: `${item.module}@${item.commit}` }))

  function mark(key: string, next: JobStatus) {
    setStatus((prev) => ({ ...prev, [key]: next }))
  }

  async function execute(toRun: AddJob[], destination: NonNullable<typeof current>) {
    setRunning(true)
    setStatus((prev) => {
      const next = { ...prev }
      for (const job of toRun) next[job.key] = { state: "pending", text: "Pending" }
      return next
    })
    let changed = false
    // One at a time: each install rewrites the same project's manifest and lock.
    for (const job of toRun) {
      mark(job.key, { state: "running", text: "Installing…" })
      try {
        await installPackage(destination.machine, destination.directory, job.spec, restart)
        mark(job.key, { state: "done", text: "Installed" })
        changed = true
      } catch (error) {
        mark(job.key, { state: "failed", text: `Couldn't install: ${errorText(error)}` })
      }
    }
    if (changed) {
      await queryClient.invalidateQueries({ queryKey: projectsQuery(destination.machine).queryKey })
    }
    setRunning(false)
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (running) return
    if (batch) {
      if (!hasResults) void execute(batch.jobs, batch.destination)
      return
    }
    const trimmed = spec.trim()
    const nextJobs: AddJob[] = [...chosenJobs]
    if (trimmed && !chosenJobs.some((job) => job.spec === trimmed)) nextJobs.push({ key: `spec:${trimmed}`, spec: trimmed })
    setBlank(nextJobs.length === 0)
    if (nextJobs.length > 0 && current) setBatch({ jobs: nextJobs, destination: current })
  }

  const failedJobs = jobs.filter((job) => status[job.key]?.state === "failed")
  const installedCount = jobs.filter((job) => status[job.key]?.state === "done").length
  const batchRows: BatchRow[] = jobs.map((job) => {
    const row = status[job.key]
    return {
      key: job.key,
      title: job.spec,
      detail: batch ? `${batch.destination.target.machineName} · ${batch.destination.directory}` : "",
      state: row?.state ?? "pending",
      statusText: row?.text ?? "",
    }
  })
  const summary = !running && hasResults
    ? [installedCount > 0 && `${installedCount} installed`, failedJobs.length > 0 && `${failedJobs.length} failed`].filter(Boolean).join(" · ")
    : undefined

  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>{batch ? hasResults ? "Package install results" : "Review packages" : fixed ? "Install package" : "Add package"}</SheetTitle>
      <SheetDescription>
        {batch ? `${batch.destination.target.machineName} · ${batch.destination.directory}` : fixed && current ? current.directory : "Installs into the project you choose."}
      </SheetDescription>
      {batch ? (
        <>
          <BatchResults label={hasResults ? "Add package results" : "Packages to install"} rows={batchRows} summary={summary} />
          {!hasResults && (
            <label className="flex min-h-6 items-center gap-(--s3) text-(length:--fs-sm) max-[760px]:min-h-11">
              <Checkbox checked={restart} onCheckedChange={setRestart} />
              Restart affected agents now
            </label>
          )}
        </>
      ) : !current && eligible.length === 0 ? (
        <Empty title="No project to add to">Projects appear here after an agent runs in them on an online machine.</Empty>
      ) : (
        <FieldGrid>
          {!fixed && (
            <Field>
              <FieldLabel>Project</FieldLabel>
              <PopupSelect label="Project" value={current?.value ?? ""} items={eligible} onValueChange={(value) => {
                setChoice(value)
                setPicked({})
              }} />
            </Field>
          )}
          {current && known.length > 0 && (
            <Field className="items-start">
              <FieldLabel className="pt-(--s1)">From Library</FieldLabel>
              <div role="group" aria-label="Known packages" className="max-h-60 min-w-0 flex-1 overflow-y-auto">
                {known.map((item) => {
                  const state = destinationState({ module: item.module, commit: item.commit, name: item.name, cache_key: "" }, current.target)
                  return (
                    <label key={item.key} className="flex min-h-6 items-center gap-(--s3) rounded-(--r-row) px-(--s2) hover:bg-(--hover) max-[760px]:min-h-11">
                      <Checkbox disabled={state.disabled} checked={Boolean(picked[item.key])} onCheckedChange={(checked) => setPicked({ ...picked, [item.key]: checked })} />
                      <span className="min-w-0 flex-1 truncate">{item.name}</span>
                      <span className="shrink-0 text-(length:--fs-sm) text-(--fg2)">{state.text}</span>
                    </label>
                  )
                })}
              </div>
            </Field>
          )}
          <Field>
            <FieldLabel htmlFor="add-spec">Package</FieldLabel>
            <Input id="add-spec" placeholder="github.com/owner/repo/path@ref" value={spec} onChange={(event) => setSpec(event.target.value)} autoFocus />
          </Field>
        </FieldGrid>
      )}
      {blank && !batch && <FieldError>Enter a package, like module@commit.</FieldError>}
      <SheetActions>
        {batch && hasResults && !running ? (
          <>
            {failedJobs.length > 0 && (
              <Button type="button" variant="primary" onClick={() => void execute(failedJobs, batch.destination)}>
                {`Retry failed (${failedJobs.length})`}
              </Button>
            )}
            <Button type="button" variant={failedJobs.length > 0 ? "default" : "primary"} onClick={onDone}>
              Done
            </Button>
          </>
        ) : (
          <>
            {batch && !running && <Button type="button" onClick={() => {
              setRestart(false)
              setBatch(undefined)
            }}>Back</Button>}
            <Button type="submit" variant="primary" disabled={running || (!batch && !current)}>
              {running ? "Installing…" : batch ? `Install ${plural(jobs.length, "package")}` : "Review packages"}
            </Button>
          </>
        )}
      </SheetActions>
    </form>
  )
}
