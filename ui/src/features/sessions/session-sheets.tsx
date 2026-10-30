import { useState, type FormEvent } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Field, FieldError, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { hud } from "@/lib/hud"
import type { ListedSession } from "./group"
import { sessionsQuery } from "./queries"

export type SheetState = { kind: "rename" | "install" | "save"; row: ListedSession }

// One Sheet for the three row dialogs. Each body mounts only while open, so its form starts clean.
export function RowSheet({ sheet, onClose }: { sheet: SheetState | null; onClose: () => void }) {
  return (
    <Sheet open={sheet !== null} onOpenChange={(open) => { if (!open) onClose() }}>
      {sheet && (
        <SheetContent>
          {sheet.kind === "rename" && <RenameForm row={sheet.row} onDone={onClose} />}
          {sheet.kind === "install" && <InstallForm row={sheet.row} onDone={onClose} />}
          {sheet.kind === "save" && <SaveForm row={sheet.row} onDone={onClose} />}
        </SheetContent>
      )}
    </Sheet>
  )
}

function RenameForm({ row, onDone }: { row: ListedSession; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(row.name ?? "")
  const [blank, setBlank] = useState(false)
  const rename = useMutation({
    mutationFn: (next: string) =>
      api<SessionInfo>(`/api/sessions/${encodeURIComponent(row.id)}`, { machine: row.machine, method: "PATCH", body: { name: next } }),
    onSuccess: (updated) => {
      queryClient.setQueryData<SessionInfo[]>(sessionsQuery(row.machine, false).queryKey, (items) =>
        items?.map((item) => (item.id === row.id ? { ...item, name: updated.name } : item)),
      )
      hud.success("Session renamed")
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    const next = name.trim()
    setBlank(next === "")
    if (next) rename.mutate(next)
  }

  const message = blank ? "Enter a name." : rename.error?.message
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Rename session</SheetTitle>
      <SheetDescription>{`${basename(row.directory)} · ${row.machineName}`}</SheetDescription>
      <FieldGrid>
        <Field>
          <FieldLabel htmlFor="session-name">Name</FieldLabel>
          <Input id="session-name" maxLength={80} value={name} onChange={(event) => setName(event.target.value)} autoFocus />
        </Field>
      </FieldGrid>
      {message && <FieldError>{message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={rename.isPending}>
          {rename.isPending ? "Renaming…" : "Rename"}
        </Button>
      </SheetActions>
    </form>
  )
}

// The gateway's install takes one module@commit spec. Picking from known packages is not in this sheet.
function InstallForm({ row, onDone }: { row: ListedSession; onDone: () => void }) {
  const [spec, setSpec] = useState("")
  const [restart, setRestart] = useState(true)
  const [blank, setBlank] = useState(false)
  const install = useMutation({
    mutationFn: (value: string) =>
      api<unknown>("/api/packages", { machine: row.machine, method: "POST", body: { directory: row.directory, spec: value, restart } }),
    onSuccess: (_result, value) => {
      hud.success(`Installed ${value}`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    const next = spec.trim()
    setBlank(next === "")
    if (next) install.mutate(next)
  }

  const message = blank ? "Enter a package, like module@commit." : install.error?.message
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Install package</SheetTitle>
      <SheetDescription>{`${row.directory} · ${row.machineName}`}</SheetDescription>
      <FieldGrid>
        <Field>
          <FieldLabel htmlFor="package-spec">Package</FieldLabel>
          <Input id="package-spec" placeholder="module@commit" value={spec} onChange={(event) => setSpec(event.target.value)} autoFocus />
        </Field>
        <Field>
          <span aria-hidden className="w-30 shrink-0" />
          <label className="flex items-center gap-(--s3) text-(--fs-sm)">
            <Checkbox checked={restart} onCheckedChange={setRestart} />
            Restart affected agents
          </label>
        </Field>
      </FieldGrid>
      {message && <FieldError>{message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={install.isPending}>
          {install.isPending ? "Installing…" : "Install"}
        </Button>
      </SheetActions>
    </form>
  )
}

// Creates a new agentpack from the folder. Adding a revision to an existing agentpack is not in this sheet.
function SaveForm({ row, onDone }: { row: ListedSession; onDone: () => void }) {
  const [name, setName] = useState(basename(row.directory))
  const [note, setNote] = useState("")
  const [blank, setBlank] = useState(false)
  const save = useMutation({
    mutationFn: (value: { name: string; label: string }) =>
      api<unknown>("/api/library/agentpacks", {
        machine: row.machine,
        method: "POST",
        body: {
          directory: row.directory,
          name: value.name,
          label: value.label || undefined,
          mode: row.mode || "default",
          auto_capture: false,
        },
      }),
    onSuccess: (_result, value) => {
      hud.success(`Saved ${value.name} to Library`)
      onDone()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    const next = name.trim()
    setBlank(next === "")
    if (next) save.mutate({ name: next, label: note.trim() })
  }

  const message = blank ? "Enter a name." : save.error?.message
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Save agentpack</SheetTitle>
      <SheetDescription>{`Capture ${row.directory} on ${row.machineName} as a new agentpack.`}</SheetDescription>
      <FieldGrid>
        <Field>
          <FieldLabel htmlFor="save-name">Name</FieldLabel>
          <Input id="save-name" maxLength={80} value={name} onChange={(event) => setName(event.target.value)} autoFocus />
        </Field>
        <Field>
          <FieldLabel htmlFor="save-note">Revision note</FieldLabel>
          <Input id="save-note" maxLength={80} placeholder="What changed?" value={note} onChange={(event) => setNote(event.target.value)} />
        </Field>
        <Field>
          <FieldLabel>Mode</FieldLabel>
          <span className="min-w-0 truncate text-(--fg2)">{row.mode || "default"}</span>
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
