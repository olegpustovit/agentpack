import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Banner, Spinner } from "@/components/kit/feedback"
import { FieldError } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent, Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import type { Draft } from "@/lib/api-types"
import { basename, plural } from "@/lib/format"
import { hud } from "@/lib/hud"
import { canApplyDraft } from "./install-state"
import { applyDraft, discardDraft, errorText, reviewDraft, type DraftReview, type PackageDiff } from "./mutations"
import { draftsQuery } from "./queries"

const signs = { add: "+", update: "~", remove: "−" } as const
const actions = { add: "Add", update: "Update", remove: "Remove" } as const

// Pending installs and restores saved while a machine or folder was unreachable. Listed above the library.
export function PendingInstalls() {
  const drafts = useQuery(draftsQuery)
  const machines = useMachines()
  const [reviewing, setReviewing] = useState<Draft>()
  const [discarding, setDiscarding] = useState<Draft>()
  const queryClient = useQueryClient()
  const discard = useMutation({
    mutationFn: (id: string) => discardDraft(id),
    onSuccess: async () => {
      setDiscarding(undefined)
      await queryClient.invalidateQueries({ queryKey: draftsQuery.queryKey })
      hud.success("Pending install discarded")
    },
    onError: (error, id) => {
      setDiscarding(undefined)
      hud.error(`Couldn't discard the pending install: ${errorText(error)}`, () => discard.mutate(id))
    },
  })

  if (drafts.error) return <Banner tone="danger">{`Pending installs unavailable: ${errorText(drafts.error)}`}</Banner>
  const list = drafts.data ?? []
  if (list.length === 0) return null

  return (
    <section aria-label="Pending installs" className="flex flex-col">
      <h2 className="flex items-center justify-between px-(--s3) py-(--s2) text-(length:--fs-sm) font-medium text-(--fg2)">
        <span>Pending installs</span>
        <span className="text-(--fg3)">{list.length}</span>
      </h2>
      {list.map((draft) => {
        const machineName = machines.find((machine) => machine.id === draft.node)?.name ?? draft.node
        const label = draft.pack_id ? `Restore ${draft.pack_id}` : draft.item
        return (
          <div key={draft.id} className="flex items-center gap-(--s2) px-(--s3) py-(--s1)">
            <div className="flex min-w-0 flex-1 flex-col">
              <span className="truncate">{label}</span>
              <span className="truncate text-(length:--fs-sm) text-(--fg2)">{`${machineName} / ${basename(draft.directory)} · ${draft.reason}`}</span>
            </div>
            <Button type="button" variant="ghost" size="sm" onClick={() => setReviewing(draft)}>
              Review…
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setDiscarding(draft)}>
              Discard
            </Button>
          </div>
        )
      })}
      <Sheet open={reviewing !== undefined} onOpenChange={(open) => !open && setReviewing(undefined)}>
        <SheetContent>
          {reviewing && <DraftReviewForm draft={reviewing} machineName={machines.find((machine) => machine.id === reviewing.node)?.name ?? reviewing.node} onDone={() => setReviewing(undefined)} />}
        </SheetContent>
      </Sheet>
      <ConfirmAlert open={discarding !== undefined} onOpenChange={(open) => !open && setDiscarding(undefined)}>
        {discarding && (
          <ConfirmAlertContent
            title={`Discard the pending install of ${discarding.pack_id ? "this restore" : discarding.item}?`}
            description="Nothing is installed or restored. Save it again from the install or restore sheet."
            destructiveLabel="Discard"
            onConfirm={() => discard.mutate(discarding.id)}
          />
        )}
      </ConfirmAlert>
    </section>
  )
}

// Shows the last review; applying always reviews again first, so a stale view never applies.
function DraftReviewForm({ draft, machineName, onDone }: { draft: Draft; machineName: string; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [restart, setRestart] = useState(true)
  const review = useQuery({ queryKey: ["drafts", draft.id, "review"], queryFn: () => reviewDraft(draft.id), gcTime: 0 })

  const apply = useMutation({
    mutationFn: async () => {
      const fresh = await reviewDraft(draft.id)
      queryClient.setQueryData<DraftReview>(["drafts", draft.id, "review"], fresh)
      if (!canApplyDraft(fresh)) throw new Error(fresh.reason || "This pending install can't be applied now. Check the destination and review it again.")
      await applyDraft(draft, restart)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: draftsQuery.queryKey })
      await queryClient.invalidateQueries({ queryKey: projectsQuery(draft.node).queryKey })
      hud.success(draft.pack_id ? "Agentpack restored" : "Package installed")
      onDone()
    },
  })

  const current = review.data
  const ready = current?.ready === true
  const label = draft.pack_id ? "Restore" : "Install"
  const busy = draft.pack_id ? "Restoring…" : "Installing…"

  function submit(event: FormEvent) {
    event.preventDefault()
    if (ready) apply.mutate()
  }

  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>{review.isPending ? "Checking pending change" : ready ? "Review pending change" : "Draft needs attention"}</SheetTitle>
      <SheetDescription>{`${draft.pack_id ? "Restore" : draft.item} · ${basename(draft.directory)} on ${machineName}`}</SheetDescription>
      {review.isPending && (
        <div className="flex items-center gap-(--s2) text-(--fg2)">
          <Spinner />
          Checking the destination…
        </div>
      )}
      {current && <p className={ready ? "text-(length:--fs-sm) text-(--fg2)" : "text-(length:--fs-sm) text-(--danger)"}>{current.reason || draft.reason}</p>}
      {current?.diff && <DiffList diff={current.diff} />}
      {ready && (
        <label className="flex items-center gap-(--s3) text-(length:--fs-sm)">
          <Checkbox checked={restart} disabled={apply.isPending} onCheckedChange={setRestart} />
          {`Restart affected agents after ${label.toLowerCase()}`}
        </label>
      )}
      {review.error && <FieldError>{`Couldn't check this pending install: ${errorText(review.error)}`}</FieldError>}
      {apply.error && <FieldError>{apply.error.message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!ready || apply.isPending}>
          {apply.isPending ? busy : label}
        </Button>
      </SheetActions>
    </form>
  )
}

function DiffList({ diff }: { diff: PackageDiff }) {
  const changes = (["add", "update", "remove"] as const).flatMap((kind) => diff[kind].map((module) => ({ kind, module })))
  if (changes.length === 0) return <p className="text-(length:--fs-sm)">No package changes</p>
  return (
    <>
      <p className="text-(length:--fs-sm) text-(--fg2)">{plural(changes.length, "change")}</p>
      <ul aria-label="Package changes" className="flex flex-col gap-(--s1) text-(length:--fs-sm)">
        {changes.map((change) => (
          <li key={`${change.kind}\0${change.module}`}>
            <span aria-hidden className="inline-block w-4">
              {signs[change.kind]}
            </span>
            {`${actions[change.kind]} ${change.module}`}
          </li>
        ))}
      </ul>
    </>
  )
}
