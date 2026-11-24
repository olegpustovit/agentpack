import { useId, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { CornerDownRight, Pencil, Send, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import { hud } from "@/lib/hud"
import { promptText, type QueuedPrompt } from "./model"
import { conversationErrorMessage, conversationNeedsUpdate } from "./queries"

// Queued messages live in the worker, so every viewer sees and can change the same queue.
export function QueueChips({ queue, machine, sessionId, readOnly, steering, steerJoins, onSteer }: {
  queue: QueuedPrompt[]; machine: string; sessionId: string; readOnly?: boolean; steering?: string; steerJoins?: boolean; onSteer: (id: string) => void
}) {
  const [editing, setEditing] = useState<{ id: string; text: string }>()
  const sendDescription = useId()
  const path = (id: string): `/api/${string}` => `/api/sessions/${encodeURIComponent(sessionId)}/conversation/queue/${encodeURIComponent(id)}`
  const change = useMutation({
    mutationFn: ({ id, action, text }: { id: string; action: "edit" | "remove"; text?: string }) =>
      action === "edit"
        ? api(path(id), { machine, method: "PATCH", body: { text } })
        : api(path(id), { machine, method: "DELETE" }),
    onSuccess: (_, input) => {
      if (input.action === "edit") setEditing(undefined)
    },
    onError: (error, input) =>
      hud.error(
        conversationErrorMessage(machine, `${input.action} a queued message`, error),
        conversationNeedsUpdate(error) ? undefined : () => change.mutate(input),
      ),
  })
  if (queue.length === 0) return null
  return (
    <ul data-slot="prompt-queue" aria-label="Queued messages" className="flex flex-col gap-(--s2)">
      {!readOnly && queue.some((item) => !item.steer && !item.delivered) && (
        <li id={sendDescription} className="px-(--s2) text-(length:--fs-xs) text-(--fg2)">
          {steerJoins ? "Send now adds the queued message to the current turn." : "Send now interrupts the current turn and sends the queued message."}
        </li>
      )}
      {queue.map((item) => {
        const text = promptText(item)
        const images = item.images?.length ?? (item.image || item.resource_link ? 1 : 0)
        const isSteering = item.steer || steering === item.queueId
        const isEditing = editing?.id === item.queueId
        return (
          <li key={item.queueId} className="flex items-center gap-(--s2) rounded-(--r-sheet) bg-(--window) px-(--s3) py-(--s2) text-(length:--fs-sm) shadow-(--shadow-composer)">
            <CornerDownRight aria-hidden className="size-(--s4) shrink-0 text-(--fg3)" />
            {isEditing ? (
              <form
                className="flex min-w-0 flex-1 items-center gap-(--s2)"
                onSubmit={(event) => {
                  event.preventDefault()
                  if (editing.text.trim()) change.mutate({ id: item.queueId, action: "edit", text: editing.text.trim() })
                }}
              >
                <input
                  aria-label="Edit queued message"
                  autoFocus
                  value={editing.text}
                  onChange={(event) => setEditing({ id: item.queueId, text: event.currentTarget.value })}
                  onKeyDown={(event) => {
                    if (event.key === "Escape") {
                      event.preventDefault()
                      setEditing(undefined)
                    }
                  }}
                  className="min-w-0 flex-1 rounded-(--r-ctl) bg-(--field) px-(--s2) py-(--s1) outline-none focus:ring-2 focus:ring-(--accent-soft)"
                />
                <Button type="submit" size="sm" disabled={change.isPending}>Save</Button>
              </form>
            ) : (
              <span className="min-w-0 flex-1 truncate" title={text}>
                <span className="text-(--fg3)">{item.delivered ? "Delivered: " : isSteering ? "Steering next: " : "Queued: "}</span>
                {text || (images ? `${images} ${images === 1 ? "image" : "images"}` : "")}
                {text && images > 0 && <span className="text-(--fg3)"> · {images} {images === 1 ? "image" : "images"}</span>}
              </span>
            )}
            {!readOnly && !isEditing && !item.delivered && (
              <>
                {!isSteering && (
                  <Button type="button" size="sm" variant="ghost" aria-describedby={sendDescription} disabled={change.isPending || steering !== undefined} onClick={() => onSteer(item.queueId)}>
                    <Send aria-hidden /> Send now
                  </Button>
                )}
                <Button type="button" size="icon" variant="ghost" aria-label="Edit queued message" disabled={change.isPending} onClick={() => setEditing({ id: item.queueId, text })}>
                  <Pencil aria-hidden />
                </Button>
                <Button type="button" size="icon" variant="ghost" aria-label="Remove queued message" disabled={change.isPending} onClick={() => change.mutate({ id: item.queueId, action: "remove" })}>
                  <X aria-hidden />
                </Button>
              </>
            )}
          </li>
        )
      })}
    </ul>
  )
}
