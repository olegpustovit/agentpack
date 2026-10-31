import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Field, FieldError, FieldGrid, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { Machine } from "@/features/machines/queries"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName } from "@/lib/format"
import { sessionsQuery, projectsQuery } from "./queries"

// Harness ids the gateway starts. agy is listed so a folder whose newest launch was agy shows its
// reason instead of an empty select.
const agents = ["claude", "codex", "grok", "omp", "opencode", "agent", "agy"].map((id) => ({
  value: id,
  label: harnessName(id),
}))

// Opened from ?new=1. The form mounts only while open, so each open starts from the defaults.
export function NewSessionSheet({
  open,
  machines,
  initialMachine,
  initialDirectory,
  onOpenChange,
}: {
  open: boolean
  machines: Machine[]
  initialMachine?: string
  initialDirectory?: string
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {open && (
        <SheetContent>
          <NewSessionForm machines={machines} initialMachine={initialMachine} initialDirectory={initialDirectory} />
        </SheetContent>
      )}
    </Sheet>
  )
}

function NewSessionForm({ machines, initialMachine, initialDirectory }: { machines: Machine[]; initialMachine?: string; initialDirectory?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const online = machines.filter((machine) => machine.online)
  // Nulls mean "use the default": the first online machine, its newest folder, and that folder's newest launch.
  const [machine, setMachine] = useState<string | null>(initialMachine ?? null)
  const [directory, setDirectory] = useState<string | null>(initialDirectory ?? null)
  const [harness, setHarness] = useState<string | null>(null)
  const [mode, setMode] = useState<string | null>(null)

  const machineId = machine ?? online[0]?.id ?? ""
  const folders = useQuery({ ...projectsQuery(machineId), enabled: machineId !== "" })
  const projects = (folders.data ?? []).filter((project) => project.available)
  const project = projects.find((item) => item.directory === directory) ?? projects[0]
  const newest = project?.launches[0]
  const harnessId = harness ?? newest?.harness ?? "claude"
  const modeValue = mode ?? newest?.mode ?? "default"

  const start = useMutation({
    mutationFn: (body: { directory: string; harness: string; mode: string }) =>
      api<SessionInfo>("/api/sessions", { machine: machineId, method: "POST", body }),
    onSuccess: (info) => {
      void queryClient.invalidateQueries({ queryKey: sessionsQuery(machineId, false).queryKey })
      void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: machineId, sessionId: info.id } })
    },
  })

  function chooseMachine(next: string) {
    setMachine(next)
    setDirectory(null)
    setHarness(null)
    setMode(null)
  }

  function chooseProject(next: string) {
    setDirectory(next)
    setHarness(null)
    setMode(null)
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (project) start.mutate({ directory: project.directory, harness: harnessId, mode: modeValue.trim() })
  }

  // One reason at a time, most blocking first.
  let hint: string | undefined
  if (online.length === 0) hint = "No machine is online."
  else if (folders.isSuccess && projects.length === 0) hint = "Folders will appear here after you launch an agent."
  else if (harnessId === "agy") hint = "Desktop launches must start locally."

  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>New session</SheetTitle>
      <SheetDescription>Start an agent in a recent folder.</SheetDescription>
      <FieldGrid>
        <Field>
          <FieldLabel>Machine</FieldLabel>
          <PopupSelect
            label="Machine"
            value={machineId}
            onValueChange={chooseMachine}
            items={online.map((item) => ({ value: item.id, label: item.name }))}
          />
        </Field>
        <Field>
          <FieldLabel>Project</FieldLabel>
          <PopupSelect
            label="Project"
            value={project?.directory ?? ""}
            onValueChange={chooseProject}
            items={projects.map((item) => ({ value: item.directory, label: item.directory }))}
          />
        </Field>
        <Field>
          <FieldLabel>Agent</FieldLabel>
          <PopupSelect label="Agent" value={harnessId} onValueChange={setHarness} items={agents} />
        </Field>
        <Field>
          <FieldLabel htmlFor="new-session-mode">Mode</FieldLabel>
          <Input id="new-session-mode" value={modeValue} onChange={(event) => setMode(event.target.value)} />
        </Field>
      </FieldGrid>
      {hint && <p className="text-(length:--fs-sm) text-(--fg2)">{hint}</p>}
      {start.error && <FieldError>{start.error.message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!project || harnessId === "agy" || start.isPending}>
          {start.isPending ? "Starting session…" : "Start session"}
        </Button>
      </SheetActions>
    </form>
  )
}
