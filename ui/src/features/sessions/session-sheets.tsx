import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Field, FieldError, FieldGrid, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { AddPackageSheet } from "@/features/library/add-package-sheet"
import { agentpacksQuery } from "@/features/library/queries"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { hud } from "@/lib/hud"
import type { ListedSession } from "./group"
import { sessionsQuery } from "./queries"

export type SaveTarget = Pick<ListedSession, "machine" | "machineName" | "directory" | "mode">
export type SheetState = { kind: "rename" | "install" | "save"; row: ListedSession }

// One Sheet for the three row dialogs. Each body mounts only while open, so its form starts clean.
export function RowSheet({ sheet, onClose }: { sheet: SheetState | null; onClose: () => void }) {
  if (sheet?.kind === "install") {
    return <AddPackageSheet open initialMachine={sheet.row.machine} initialDirectory={sheet.row.directory} onOpenChange={(open) => { if (!open) onClose() }} />
  }
  return (
    <Sheet open={sheet !== null} onOpenChange={(open) => { if (!open) onClose() }}>
      {sheet && (
        <SheetContent>
          {sheet.kind === "rename" && <RenameForm row={sheet.row} onDone={onClose} />}
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

// Captures the folder as a new agentpack, or as a new revision of one this machine holds.
export function SaveForm({ row, onDone }: { row: SaveTarget; onDone: () => void }) {
  const [name, setName] = useState(basename(row.directory))
  const [note, setNote] = useState("")
  const [blank, setBlank] = useState(false)
  const [target, setTarget] = useState("new")
  const packs = useQuery(agentpacksQuery(row.machine)).data ?? []
  const existing = packs.find((pack) => pack.agentpack.id === target)?.agentpack
  const save = useMutation({
    mutationFn: (value: { name: string; label: string }) =>
      api<unknown>("/api/library/agentpacks", {
        machine: row.machine,
        method: "POST",
        body: {
          directory: row.directory,
          name: value.name,
          agentpack_id: existing?.id,
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
    const next = existing?.name ?? name.trim()
    setBlank(next === "")
    if (next) save.mutate({ name: next, label: note.trim() })
  }

  const message = blank ? "Enter a name." : save.error?.message
  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Save agentpack</SheetTitle>
      <SheetDescription>{`Capture ${row.directory} on ${row.machineName}.`}</SheetDescription>
      <FieldGrid>
        {packs.length > 0 && (
          <Field>
            <FieldLabel>Save to</FieldLabel>
            <PopupSelect
              label="Save to"
              value={target}
              items={[{ value: "new", label: "New agentpack" }, ...packs.map((pack) => ({ value: pack.agentpack.id, label: pack.agentpack.name }))]}
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
