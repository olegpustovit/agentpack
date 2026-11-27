import { useState, type ComponentProps, type FormEvent, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Field, FieldError, FieldGrid, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { agentpacksQuery } from "@/features/library/queries"
import { api, LOCAL } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { hud } from "@/lib/hud"
import type { ListedSession } from "./group"
import { projectHistoryKey, sessionHistoryKey, sessionsQuery } from "./queries"

export type SaveTarget = Pick<ListedSession, "machine" | "machineName" | "directory" | "mode">
export type SheetState = { kind: "rename"; row: ListedSession }

// Each dialog body mounts only while open, so its form starts clean.
export function RowSheet({ sheet, onClose, finalFocus }: {
  sheet: SheetState | null
  onClose: () => void
  finalFocus?: ComponentProps<typeof SheetContent>["finalFocus"]
}) {
  return (
    <Sheet open={sheet !== null} onOpenChange={(open) => { if (!open) onClose() }}>
      {sheet && (
        <SheetContent className="w-90" {...(finalFocus !== undefined && { finalFocus })}>
          <RenameForm row={sheet.row} onDone={onClose} />
        </SheetContent>
      )}
    </Sheet>
  )
}

function RenameForm({ row, onDone }: { row: ListedSession; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(row.name || row.title || "")
  const [blank, setBlank] = useState(false)
  const listKey = sessionsQuery(false).queryKey
  const withName = (items: SessionInfo[] | undefined, name: string | undefined) =>
    items?.map((item) => item.id === row.id ? { ...item, name } : item)
  const setListedName = (name: string | undefined) =>
    queryClient.setQueryData<SessionInfo[]>(listKey, (items) => withName(items, name))
  const rename = useMutation({
    mutationFn: (next: string) =>
      api<SessionInfo>(`/api/sessions/${encodeURIComponent(row.id)}`, { machine: row.machine, method: "PATCH", body: { name: next } }),
    // The list shows the new name at once; the sheet stays open so a refusal still shows under the field.
    // A refetch already in flight would land the old name over the optimistic one.
    onMutate: async (next) => {
      await queryClient.cancelQueries({ queryKey: listKey, exact: true })
      const previous = queryClient.getQueryData<SessionInfo[]>(listKey)?.find((item) => item.id === row.id)?.name
      setListedName(next)
      return { previous }
    },
    onError: (_, __, context) => setListedName(context?.previous),
    onSuccess: (updated) => {
      setListedName(updated.name)
      queryClient.setQueriesData<SessionInfo[]>({ queryKey: projectHistoryKey }, (items) => withName(items, updated.name))
      void queryClient.invalidateQueries({ queryKey: projectHistoryKey })
      void queryClient.invalidateQueries({ queryKey: sessionHistoryKey })
      void queryClient.invalidateQueries({ queryKey: ["m", LOCAL, "session-row", row.machine, row.id] })
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
        <Field className="flex-col items-stretch gap-(--s3)">
          <FieldLabel htmlFor="session-name" className="w-auto text-left">Name</FieldLabel>
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
export function SaveForm({ row, onDone, children }: { row: SaveTarget; onDone: () => void; children?: ReactNode }) {
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
      <SheetDescription>{existing ? `Adds a revision to ${existing.name}, from the ${row.mode || "default"} mode.` : `A new agentpack from the ${row.mode || "default"} mode of this folder.`}</SheetDescription>
      <FieldGrid>
        {children}
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
