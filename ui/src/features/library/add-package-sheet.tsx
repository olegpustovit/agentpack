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
import { SaveForm } from "@/features/sessions/session-sheets"
import { basename, plural } from "@/lib/format"
import { hud } from "@/lib/hud"
import { destinationState } from "./install-state"
import type { PackageItem } from "./model"
import { errorText, installPackage } from "./mutations"
import { useLibrary } from "./use-library"

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

// Save agentpack from the Library toolbar: choose a project first, then the same form a session row opens.
export function SaveAgentpackSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const [choice, setChoice] = useState<string>()
  const eligible = machines.filter((machine) => machine.online).flatMap((machine) =>
    (projects.byMachine[machine.id]?.data ?? [])
      .filter((project) => project.available)
      .map((project) => ({ value: `${machine.id}\0${project.directory}`, label: `${machine.name} · ${project.name || basename(project.directory)}`, machine, project })),
  )
  const current = eligible.find((entry) => entry.value === choice) ?? eligible[0]
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        {current ? (
          <>
            <Field>
              <FieldLabel>Project</FieldLabel>
              <PopupSelect label="Project" value={current.value} items={eligible} onValueChange={setChoice} />
            </Field>
            <SaveForm
              key={current.value}
              row={{ machine: current.machine.id, machineName: current.machine.name, directory: current.project.directory, mode: current.project.launches[0]?.mode ?? "default" }}
              onDone={() => onOpenChange(false)}
            />
          </>
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

function AddPackageForm({ onDone, initialMachine, initialDirectory }: { onDone: () => void; initialMachine?: string; initialDirectory?: string }) {
  const queryClient = useQueryClient()
  const machines = useMachines()
  const { items } = useLibrary()
  const projects = useMachineQueries(projectsQuery)
  const [spec, setSpec] = useState("")
  const [picked, setPicked] = useState<Record<string, boolean>>({})
  const [restart, setRestart] = useState(true)
  const [choice, setChoice] = useState<string | undefined>(initialMachine && initialDirectory ? `${initialMachine}\0${initialDirectory}` : undefined)
  const [blank, setBlank] = useState(false)
  const fixed = Boolean(initialDirectory)

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
          target: { machine: machine.id, machineName: machine.name, online: machine.online, project },
        })),
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
          project: { directory: initialDirectory, name: basename(initialDirectory), folder_id: "", available: true, unavailable_reason: "", last_used: "", launches: [], packages: [], mcp: [], error: "" },
        },
      }
    : undefined
  const current = listed ?? unlisted ?? eligible[0]
  const known = items.filter((item): item is PackageItem => item.type === "skill" || item.type === "plugin")
  const chosen = known.filter((item) => picked[item.key]).map((item) => `${item.module}@${item.commit}`)

  const add = useMutation({
    mutationFn: async (specs: string[]) => {
      if (!current) throw new Error("Choose a project to install into.")
      // One at a time: each install rewrites the same project's manifest and lock.
      for (const value of specs) await installPackage(current.machine, current.directory, value, restart)
    },
    onSuccess: async (_result, specs) => {
      if (current) await queryClient.invalidateQueries({ queryKey: projectsQuery(current.machine).queryKey })
      hud.success(specs.length === 1 ? `Added ${specs[0]}` : `Added ${plural(specs.length, "package")}`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    const specs = [...chosen, ...(spec.trim() ? [spec.trim()] : [])]
    setBlank(specs.length === 0)
    if (specs.length > 0 && current) add.mutate(specs)
  }

  const message = blank ? "Enter a package, like module@commit." : add.error ? `Couldn't add the package: ${errorText(add.error)}` : undefined
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>{fixed ? "Install package" : "Add package"}</SheetTitle>
      <SheetDescription>{fixed && current ? current.directory : "Installs into the project you choose."}</SheetDescription>
      {!current ? (
        <Empty title="No project to add to">Projects appear here after an agent runs in them on an online machine.</Empty>
      ) : (
        <FieldGrid>
          {!fixed && (
            <Field>
              <FieldLabel>Project</FieldLabel>
              <PopupSelect label="Project" value={current?.value ?? ""} items={eligible} onValueChange={setChoice} />
            </Field>
          )}
          {current && known.length > 0 && (
            <div role="group" aria-label="Known packages" className="max-h-60 overflow-y-auto">
              {known.map((item) => {
                const state = destinationState({ module: item.module, commit: item.commit, name: item.name, cache_key: "" }, current.target)
                return (
                  <label key={item.key} className="flex h-(--row-h) items-center gap-(--s3) rounded-(--r-row) px-(--s2) hover:bg-(--hover)">
                    <Checkbox disabled={state.disabled} checked={Boolean(picked[item.key])} onCheckedChange={(checked) => setPicked({ ...picked, [item.key]: checked })} />
                    <span className="min-w-0 flex-1 truncate">{item.name}</span>
                    <span className="shrink-0 text-(length:--fs-sm) text-(--fg2)">{state.text}</span>
                  </label>
                )
              })}
            </div>
          )}
          <Field>
            <FieldLabel htmlFor="add-spec">Package</FieldLabel>
            <Input id="add-spec" placeholder="module@commit" value={spec} onChange={(event) => setSpec(event.target.value)} autoFocus />
          </Field>
          <Field>
            <span aria-hidden className="w-30 shrink-0" />
            <label className="flex items-center gap-(--s3) text-(length:--fs-sm)">
              <Checkbox checked={restart} onCheckedChange={setRestart} />
              Restart affected agents
            </label>
          </Field>
        </FieldGrid>
      )}
      {message && <FieldError>{message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!current || add.isPending}>
          {add.isPending ? "Adding…" : fixed ? "Install" : "Add package"}
        </Button>
      </SheetActions>
    </form>
  )
}
