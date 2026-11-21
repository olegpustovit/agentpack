import type { ReactNode } from "react"
import { ArrowUpRight, Bot, Check, Square, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/kit/feedback"
import { clock } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import type { Subagent, ThreadItem } from "./model"

export function compactCount(value: number): string {
  return value >= 1000 ? `${(value / 1000).toFixed(value >= 10_000 ? 0 : 1)}k` : String(value)
}

export function subagentRunning(subagent: Subagent): boolean {
  return subagent.state !== "idle"
}

export function subagentPending(subagent: Subagent): ThreadItem | undefined {
  return subagent.thread.items.findLast((item) => {
    const data = item.kind === "permission" ? (item.data as { requestId?: string; resolved?: boolean; inactive?: boolean }) : undefined
    return Boolean(data?.requestId && !data.resolved && !data.inactive)
  })
}

function subagentTitle(subagent: Subagent, fallback?: string): string {
  return subagent.title || fallback || "Subagent"
}

// Summary line: "✓ 41s · 18 tools · 22k tokens" once done, live counters while running.
export function subagentStats(subagent: Subagent, now: number): string {
  const end = subagent.endTs ?? now
  const parts = [
    subagent.startedTs !== undefined ? clock(Math.max(0, end - subagent.startedTs)) : undefined,
    // A helper chat's tools are in its own chat, not counted here.
    subagent.id.startsWith("helper:") ? undefined : `${subagent.tools} ${subagent.tools === 1 ? "tool" : "tools"}`,
    subagent.tokens ? `${compactCount(subagent.tokens)} tokens` : undefined,
  ]
  return parts.filter(Boolean).join(" · ")
}

// Shown where the subagent was spawned. Approvals it waits on render inside the card.
export function SubagentCard({
  subagent,
  toolTitle,
  renderPermission,
  onOpen,
  onStop,
}: {
  subagent: Subagent
  toolTitle?: string
  renderPermission: (item: ThreadItem) => ReactNode
  onOpen?: () => void
  onStop?: () => void
}) {
  const running = subagentRunning(subagent)
  const now = useNow(running)
  const pending = subagentPending(subagent)
  const title = subagentTitle(subagent, toolTitle)
  const failed = subagent.stopReason === "error"
  const cancelled = subagent.stopReason === "cancelled"
  return (
    <div
      data-slot="subagent-card"
      data-state={pending ? "requires_action" : subagent.state}
      className={`rounded-(--r-sheet) border-[0.5px] bg-(--window) shadow-(--shadow-card) ${pending ? "border-(--warn)" : "border-(--sep)"}`}
    >
      <div className="flex min-h-(--row-h-2) items-center gap-(--s3) px-(--s4) py-(--s2)">
        <span aria-hidden className="grid size-6 shrink-0 place-items-center rounded-[6px] bg-(--track) text-(--fg2)">
          <Bot className="size-3.5" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-(length:--fs-sm) font-medium text-(--fg)">{title}</p>
          {subagent.description && <p className="truncate text-(length:--fs-xs) text-(--fg2)">{subagent.description}</p>}
        </div>
        {running && !pending && <Spinner aria-label="Running" />}
        {!running && (
          <span role="img" aria-label={failed ? "Failed" : cancelled ? "Stopped" : "Done"} className={failed ? "text-(--danger-word)" : "text-(--ok-word)"}>
            {failed || cancelled ? <X aria-hidden className="size-3.5" /> : <Check aria-hidden className="size-3.5" />}
          </span>
        )}
        {running && onStop && subagent.cancellable && (
          <Button type="button" size="icon" variant="ghost" aria-label={`Stop ${title}`} title="Stop subagent" onClick={onStop}>
            <Square aria-hidden className="fill-current" />
          </Button>
        )}
        {onOpen && (
          <Button type="button" size="icon" variant="ghost" aria-label={`Open ${title}`} title="Open subagent" onClick={onOpen}>
            <ArrowUpRight aria-hidden />
          </Button>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-(--s4) gap-y-(--s1) border-t-[0.5px] border-(--sep) px-(--s4) py-(--s2) text-(length:--fs-xs) text-(--fg3)">
        {running && subagent.lastTool && (
          <span className="min-w-0 max-w-full truncate text-(--fg2)">now: {subagent.lastTool}</span>
        )}
        <span className="tabular-nums">{subagentStats(subagent, now)}</span>
      </div>
      {pending && <div className="border-t-[0.5px] border-(--sep) p-(--s3)">{renderPermission(pending)}</div>}
    </div>
  )
}
