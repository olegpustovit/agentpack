import { ApiError, errorText } from "@/lib/api"
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
import { plural } from "@/lib/format"
import { hud } from "@/lib/hud"
import { canApplyDraft } from "./install-state"
import type { LibraryInput } from "./model"
import { applyDraft, discardDraft, reviewDraft, type DraftReview, type PackageDiff } from "./mutations"
import { draftsQuery } from "./queries"
import { RestoreRestartChoice } from "./restore-sheet"
import { useLibrary } from "./use-library"

const signs = { add: "+", update: "~", remove: "−" } as const
const actions = { add: "Add", update: "Update", remove: "Remove" } as const

function describeDraft(draft: Draft, inputs: LibraryInput[]): { title: string; status: "Ready" | "Offline" | "Needs attention" } {
  let title = draft.item
  if (draft.pack_id) {
    let packName = ""
    let revLabel = ""
    for (const input of inputs) {
      const entry = input.agentpacks.find((item) => item.agentpack.id === draft.pack_id)
      if (!entry) continue
      packName = packName || entry.agentpack.name
      if (draft.revision) {
        const rev = (entry.revisions ?? []).find((item) => item.id === draft.revision)
        if (rev?.label) revLabel = rev.label
      }
    }
    const displayName = packName || draft.pack_id
    title = revLabel ? `Restore ${displayName} · ${revLabel}` : `Restore ${displayName}`
  }
  const input = inputs.find((entry) => entry.machine.id === draft.node)
  if (!input || input.machine.online === false) return { title, status: "Offline" }
  const project = input.projects.find((entry) => entry.directory === draft.directory)
  if (draft.needed_holder || !project?.available || Boolean(project.error)) {
    return { title, status: "Needs attention" }
  }
  return { title, status: "Ready" }
}

// Pending changes saved while a machine or folder was unreachable. Listed above the library.
export function PendingInstalls() {
  const drafts = useQuery(draftsQuery)
  const machines = useMachines()
  const { inputs } = useLibrary()
  const [reviewing, setReviewing] = useState<Draft>()
  const [discarding, setDiscarding] = useState<Draft>()
  const queryClient = useQueryClient()
  const discard = useMutation({
    mutationFn: (id: string) => discardDraft(id),
    onSuccess: async () => {
      setDiscarding(undefined)
      await queryClient.invalidateQueries({ queryKey: draftsQuery.queryKey })
      hud.success("Pending change discarded")
    },
    onError: (error, id) => {
      setDiscarding(undefined)
      hud.error(`Couldn't discard the pending change: ${errorText(error)}`, () => discard.mutate(id))
    },
  })

  if (drafts.error) return <Banner tone="danger">{`Pending changes unavailable: ${errorText(drafts.error)}`}</Banner>
  const list = drafts.data ?? []
  if (list.length === 0) return null

  const reviewingInfo = reviewing ? describeDraft(reviewing, inputs) : undefined
  const discardingInfo = discarding ? describeDraft(discarding, inputs) : undefined

  return (
    <section aria-label="Pending changes" className="flex flex-col">
      <h2 className="flex items-center justify-between px-(--s3) pt-(--s2) text-(length:--fs-sm) font-medium text-(--fg2)">
        <span>{`Pending changes (${list.length})`}</span>
      </h2>
      <p className="px-(--s3) pb-(--s2) text-(length:--fs-xs) text-(--fg3)">Reconnecting never applies a draft.</p>
      {list.map((draft) => {
        const machineName = machines.find((machine) => machine.id === draft.node)?.name ?? draft.node
        const { title, status } = describeDraft(draft, inputs)
        const cachedReview = queryClient.getQueryData<DraftReview>(["drafts", draft.id, "review"])
        const displayStatus = cachedReview && (!canApplyDraft(cachedReview) || !cachedReview.pins_match) && status !== "Offline" ? "Needs attention" : status
        return (
          <div key={draft.id} className="flex flex-wrap items-center gap-(--s2) px-(--s3) py-(--s2)">
            <div className="flex min-w-0 flex-1 flex-col">
              <span className="break-words font-medium">{title}</span>
              <span className="break-all text-(length:--fs-xs) text-(--fg2)" title={draft.directory}>{`${machineName} · ${draft.directory}`}</span>
              <span className="text-(length:--fs-xs) text-(--fg2)">
                {displayStatus}
                {draft.reason ? ` · ${draft.reason}` : ""}
              </span>
            </div>
            <div className="flex shrink-0 items-center gap-(--s1)">
              <Button type="button" variant="ghost" size="sm" onClick={() => setReviewing(draft)}>
                Review again
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={() => setDiscarding(draft)}>
                Discard
              </Button>
            </div>
          </div>
        )
      })}
      <Sheet open={reviewing !== undefined} onOpenChange={(open) => !open && setReviewing(undefined)}>
        <SheetContent>
          {reviewing && (
            <DraftReviewForm
              draft={reviewing}
              title={reviewingInfo?.title ?? reviewing.item}
              machineName={machines.find((machine) => machine.id === reviewing.node)?.name ?? reviewing.node}
              onDone={() => setReviewing(undefined)}
            />
          )}
        </SheetContent>
      </Sheet>
      <ConfirmAlert open={discarding !== undefined} onOpenChange={(open) => !open && setDiscarding(undefined)}>
        {discarding && (
          <ConfirmAlertContent
            title={`Discard the pending change for ${discardingInfo?.title ?? discarding.item}?`}
            description="Nothing is installed or restored. Save it again from the install or restore sheet."
            destructiveLabel="Discard"
            onConfirm={() => discard.mutate(discarding.id)}
          />
        )}
      </ConfirmAlert>
    </section>
  )
}

// Confirmation is bound to the exact reviewed diff: if a recheck sees a different diff, require confirming again.
function DraftReviewForm({ draft, title, machineName, onDone }: { draft: Draft; title: string; machineName: string; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [restart, setRestart] = useState(false)
  const [changedSinceReview, setChangedSinceReview] = useState(false)
  const review = useQuery({
    queryKey: ["drafts", draft.id, "review"],
    queryFn: () => reviewDraft(draft.id),
    gcTime: 0,
    staleTime: "static",
  })

  const current = review.data
  const activeDigest = current?.diff?.preview_digest

  async function recheck() {
    const fresh = await review.refetch()
    if (!fresh.isError) setChangedSinceReview(activeDigest !== undefined && fresh.data?.diff?.preview_digest !== activeDigest)
  }

  const apply = useMutation({
    mutationFn: async () => {
      if (!activeDigest) throw new Error("Needs update")
      const { data: fresh, isError } = await review.refetch()
      if (isError) return "changed" as const
      if (fresh?.diff?.preview_digest !== activeDigest) {
        setChangedSinceReview(true)
        return "changed" as const
      }
      if (!canApplyDraft(fresh)) {
        throw new Error(fresh.reason || "This pending change can't be applied now. Review the destination again.")
      }
      try {
        await applyDraft(draft, restart, activeDigest)
        return "applied" as const
      } catch (error) {
        if (error instanceof ApiError && error.status === 409) {
          setChangedSinceReview(true)
          await review.refetch()
          return "changed" as const
        }
        throw error
      }
    },
    onSuccess: async (outcome) => {
      if (outcome === "changed") return
      await queryClient.invalidateQueries({ queryKey: draftsQuery.queryKey })
      await queryClient.invalidateQueries({ queryKey: projectsQuery(draft.node).queryKey })
      hud.success(draft.pack_id ? "Agentpack restored" : "Package installed")
      onDone()
    },
  })

  const ready = current !== undefined && !review.isError && canApplyDraft(current)
  const needsUpdate = current?.ready === true && !activeDigest
  const label = draft.pack_id ? "Restore" : "Install"
  const busy = draft.pack_id ? "Restoring…" : "Installing…"

  function submit(event: FormEvent) {
    event.preventDefault()
    if (ready && !review.isFetching && !apply.isPending) apply.mutate()
  }

  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>{review.isPending ? "Checking pending change" : ready ? "Review pending change" : "Draft needs attention"}</SheetTitle>
      <SheetDescription>{`${title} · ${machineName} · ${draft.directory}`}</SheetDescription>
      {changedSinceReview && (
        <Banner tone="warn">Changed since you reviewed. Check the updated changes below and confirm again.</Banner>
      )}
      {review.isPending && (
        <div className="flex items-center gap-(--s2) text-(--fg2)">
          <Spinner />
          Checking the destination…
        </div>
      )}
      {current?.ready && !current.pins_match ? (
        <Banner tone="warn">Destination pins changed since the draft was saved. Review the current changes below.</Banner>
      ) : current && (
        <p className={ready ? "text-(length:--fs-sm) text-(--fg2)" : "text-(length:--fs-sm) text-(--danger)"}>{current.reason || draft.reason}</p>
      )}
      {needsUpdate && <Banner tone="warn">Needs update. Update the destination gateway to apply this draft.</Banner>}
      {current?.diff && <DiffList diff={current.diff} />}
      {ready && draft.pack_id && (
        <RestoreRestartChoice machine={draft.node} directory={draft.directory} restart={restart} disabled={apply.isPending} onChange={setRestart} />
      )}
      {ready && !draft.pack_id && (
        <label className="flex min-h-6 items-center gap-(--s3) text-(length:--fs-sm) max-[760px]:min-h-11">
          <Checkbox checked={restart} disabled={apply.isPending} onCheckedChange={setRestart} />
          Restart affected agents after install
        </label>
      )}
      {review.error && <FieldError>{`Couldn't check this pending change: ${errorText(review.error)}`}</FieldError>}
      {apply.error && <FieldError>{apply.error.message}</FieldError>}
      <SheetActions>
        <Button type="button" disabled={review.isFetching || apply.isPending} onClick={() => void recheck()}>
          {review.isFetching ? "Checking…" : "Review again"}
        </Button>
        <Button type="submit" variant="primary" disabled={!ready || review.isFetching || apply.isPending}>
          {apply.isPending ? busy : changedSinceReview ? `Confirm ${label.toLowerCase()}` : label}
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
