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
import { errorText, installPackage } from "./mutations"

// Opened from ?add=1 on the Library, or from a project row with that project preselected.
export function AddPackageSheet({
  open,
  initialMachine,
  initialDirectory,
  onOpenChange,
}: {
  open: boolean
  initialMachine?: string
  initialDirectory?: string
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        <AddPackageForm onDone={() => onOpenChange(false)} initialMachine={initialMachine} initialDirectory={initialDirectory} />
      </SheetContent>
    </Sheet>
  )
}

function AddPackageForm({ onDone, initialMachine, initialDirectory }: { onDone: () => void; initialMachine?: string; initialDirectory?: string }) {
  const queryClient = useQueryClient()
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const [spec, setSpec] = useState("")
  const [choice, setChoice] = useState<string | undefined>(initialMachine && initialDirectory ? `${initialMachine}\0${initialDirectory}` : undefined)
  const [blank, setBlank] = useState(false)

  const eligible = machines
    .filter((machine) => machine.online)
    .flatMap((machine) =>
      (projects.byMachine[machine.id]?.data ?? [])
        .filter((project) => project.available)
        .map((project) => ({
          value: `${machine.id}\0${project.directory}`,
          machine: machine.id,
          directory: project.directory,
          label: `${machine.name} · ${project.name || basename(project.directory)}`,
        })),
    )
  const current = eligible.find((entry) => entry.value === choice) ?? eligible[0]

  const add = useMutation({
    mutationFn: (value: string) => {
      if (!current) throw new Error("Choose a project to install into.")
      return installPackage(current.machine, current.directory, value, true)
    },
    onSuccess: async (_result, value) => {
      if (current) await queryClient.invalidateQueries({ queryKey: projectsQuery(current.machine).queryKey })
      hud.success(`Added ${value}`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    const next = spec.trim()
    setBlank(next === "")
    if (next && current) add.mutate(next)
  }

  const message = blank ? "Enter a package, like module@commit." : add.error ? `Couldn't add the package: ${errorText(add.error)}` : undefined
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Add package</SheetTitle>
      <SheetDescription>Installs into the project you choose. Affected agents restart.</SheetDescription>
      {eligible.length === 0 ? (
        <Empty title="No project to add to">Projects appear here after an agent runs in them on an online machine.</Empty>
      ) : (
        <FieldGrid>
          <Field>
            <FieldLabel htmlFor="add-spec">Package</FieldLabel>
            <Input id="add-spec" placeholder="module@commit" value={spec} onChange={(event) => setSpec(event.target.value)} autoFocus />
          </Field>
          <Field>
            <FieldLabel>Project</FieldLabel>
            <PopupSelect label="Project" value={current?.value ?? ""} items={eligible} onValueChange={setChoice} />
          </Field>
        </FieldGrid>
      )}
      {message && <FieldError>{message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!current || add.isPending}>
          {add.isPending ? "Adding…" : "Add package"}
        </Button>
      </SheetActions>
    </form>
  )
}
