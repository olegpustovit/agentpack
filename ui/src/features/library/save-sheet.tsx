import { useState, type FormEvent } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Empty } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import { basename } from "@/lib/format"
import { hud } from "@/lib/hud"
import { errorText, renameAgentpack, saveRevision } from "./mutations"

// Opened from ?save=1 on an agentpack. Captures one available project as a new revision of it.
export function SaveRevisionSheet({
  packId,
  packName,
  holders,
  open,
  onOpenChange,
}: {
  packId: string
  packName: string
  holders: string[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        <SaveRevisionForm packId={packId} packName={packName} holders={holders} onDone={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}

function SaveRevisionForm({ packId, packName, holders, onDone }: { packId: string; packName: string; holders: string[]; onDone: () => void }) {
  const queryClient = useQueryClient()
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const [choice, setChoice] = useState<string>()
  const [note, setNote] = useState("")

  const eligible = machines
    .filter((machine) => machine.online && holders.includes(machine.id))
    .flatMap((machine) =>
      (projects.byMachine[machine.id]?.data ?? [])
        .filter((project) => project.available)
        .map((project) => ({ value: `${machine.id}\0${project.directory}`, machine: machine.id, project, label: `${machine.name} · ${project.name || basename(project.directory)}` })),
    )
  const current = eligible.find((entry) => entry.value === choice) ?? eligible[0]

  const save = useMutation({
    mutationFn: async () => {
      if (!current) throw new Error("No available project on a machine that holds this agentpack.")
      return saveRevision(current.machine, {
        directory: current.project.directory,
        name: packName,
        agentpack_id: packId,
        label: note.trim() || undefined,
        mode: current.project.launches[0]?.mode || "default",
        auto_capture: false,
      })
    },
    onSuccess: async () => {
      if (current) await queryClient.invalidateQueries({ queryKey: ["m", current.machine, "library"] })
      hud.success(`Saved a new revision of ${packName}`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    if (current) save.mutate()
  }

  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Save revision</SheetTitle>
      <SheetDescription>{`Capture a project as a new revision of ${packName}.`}</SheetDescription>
      {eligible.length === 0 ? (
        <Empty title="No project to capture from">A project on a machine that holds this agentpack must be online and available.</Empty>
      ) : (
        <FieldGrid>
          <Field>
            <FieldLabel>Project</FieldLabel>
            <PopupSelect label="Project" value={current?.value ?? ""} items={eligible} onValueChange={setChoice} />
          </Field>
          <Field>
            <FieldLabel htmlFor="revision-note">Revision note</FieldLabel>
            <Input id="revision-note" maxLength={80} placeholder="What changed?" value={note} onChange={(event) => setNote(event.target.value)} autoFocus />
          </Field>
        </FieldGrid>
      )}
      {save.error && <FieldError>{`Couldn't save the revision: ${errorText(save.error)}`}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!current || save.isPending}>
          {save.isPending ? "Saving…" : "Save revision"}
        </Button>
      </SheetActions>
    </form>
  )
}

// Opened from ?rename=1. The label changes on every holding machine that is online; identity and revisions stay.
export function RenameSheet({
  packId,
  packName,
  holders,
  open,
  onOpenChange,
}: {
  packId: string
  packName: string
  holders: string[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        <RenameForm packId={packId} packName={packName} holders={holders} onDone={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}

function RenameForm({ packId, packName, holders, onDone }: { packId: string; packName: string; holders: string[]; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(packName)
  const [blank, setBlank] = useState(false)

  const rename = useMutation({
    mutationFn: (next: string) => Promise.all(holders.map((machine) => renameAgentpack(machine, packId, next))),
    onSuccess: async (_result, next) => {
      for (const machine of holders) await queryClient.invalidateQueries({ queryKey: ["m", machine, "library"] })
      hud.success(`Renamed to ${next}`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    const next = name.trim()
    setBlank(next === "")
    if (next) rename.mutate(next)
  }

  const message = blank ? "Enter a name." : rename.error ? `Couldn't rename: ${errorText(rename.error)}` : undefined
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Rename agentpack</SheetTitle>
      <SheetDescription>The Library label changes. The agentpack identity and its revisions stay the same.</SheetDescription>
      <FieldGrid>
        <Field>
          <FieldLabel htmlFor="rename-name">Name</FieldLabel>
          <Input id="rename-name" maxLength={80} value={name} onChange={(event) => setName(event.target.value)} autoFocus />
        </Field>
      </FieldGrid>
      {message && <FieldError>{message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={rename.isPending || holders.length === 0}>
          {rename.isPending ? "Renaming…" : "Rename"}
        </Button>
      </SheetActions>
    </form>
  )
}
