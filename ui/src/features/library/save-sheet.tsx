import { errorText } from "@/lib/api"
import { useState, type FormEvent, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Empty } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import { basename } from "@/lib/format"
import { hud } from "@/lib/hud"
import { renameAgentpack, saveRevision } from "./mutations"
import { agentpacksQuery } from "./queries"

// Library-owned save form with explicit source (machine › full path) and shared vocabulary:
// New saved configuration vs Save new revision.
export function SavePackForm({
  machine,
  machineName,
  directory,
  mode,
  onDone,
  children,
}: {
  machine: string
  machineName: string
  directory: string
  mode: string
  onDone: () => void
  children?: ReactNode
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(basename(directory))
  const [note, setNote] = useState("")
  const [blank, setBlank] = useState(false)
  const [target, setTarget] = useState("new")
  const packs = useQuery(agentpacksQuery(machine)).data ?? []
  const existing = packs.find((pack) => pack.agentpack.id === target)?.agentpack

  const save = useMutation({
    mutationFn: (value: { name: string; label: string }) =>
      saveRevision(machine, {
        directory,
        name: value.name,
        agentpack_id: existing?.id,
        label: value.label || undefined,
        mode: mode || "default",
        auto_capture: false,
      }),
    onSuccess: async (_result, value) => {
      await queryClient.invalidateQueries({ queryKey: ["m", machine, "library"] })
      hud.success(existing ? `Saved new revision of ${value.name}` : `Saved ${value.name} to Library`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    const next = existing?.name ?? name.trim()
    setBlank(next === "")
    if (next) save.mutate({ name: next, label: note.trim() })
  }

  const message = blank ? "Enter a name." : save.error ? `Couldn't save: ${errorText(save.error)}` : undefined
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>{existing ? "Save new revision" : "New saved configuration"}</SheetTitle>
      <SheetDescription>
        {`${machineName} › ${directory} · ${existing ? `Saves a new revision to ${existing.name} (${mode || "default"} mode).` : `Saves a new configuration from ${mode || "default"} mode.`}`}
      </SheetDescription>
      <FieldGrid>
        {children}
        {packs.length > 0 && (
          <Field>
            <FieldLabel>Destination</FieldLabel>
            <PopupSelect
              label="Destination"
              value={target}
              items={[
                { value: "new", label: "New saved configuration" },
                ...packs.map((pack) => ({ value: pack.agentpack.id, label: `Save new revision · ${pack.agentpack.name}` })),
              ]}
              onValueChange={setTarget}
            />
          </Field>
        )}
        {!existing && (
          <Field>
            <FieldLabel htmlFor="save-name">Name</FieldLabel>
            <Input id="save-name" maxLength={80} value={name} onChange={(event) => setName(event.target.value)} autoFocus />
          </Field>
        )}
        <Field>
          <FieldLabel htmlFor="save-note">Revision note</FieldLabel>
          <Input id="save-note" maxLength={80} placeholder="What changed?" value={note} onChange={(event) => setNote(event.target.value)} />
        </Field>
      </FieldGrid>
      {message && <FieldError>{message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={save.isPending}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </SheetActions>
    </form>
  )
}

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
        .map((project) => ({
          value: `${machine.id}\0${project.directory}`,
          machine: machine.id,
          machineName: machine.name,
          project,
          label: `${machine.name} · ${project.name || basename(project.directory)} (${project.directory})`,
        })),
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
      <SheetTitle>Save new revision</SheetTitle>
      <SheetDescription>
        {current
          ? `Source: ${current.machineName} › ${current.project.directory} → Destination: ${packName}.`
          : `Capture a project as a new revision of ${packName}.`}
      </SheetDescription>
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
          {save.isPending ? "Saving…" : "Save new revision"}
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
    onSuccess: async () => {
      for (const machine of holders) await queryClient.invalidateQueries({ queryKey: ["m", machine, "library"] })
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
      <SheetDescription>{`Changes the Library name for ${packName}. The saved configuration identity and its revisions stay the same.`}</SheetDescription>
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
