import { useState, type ReactNode } from "react"
import { FileCode2 } from "lucide-react"
import { Section } from "@/components/kit/data"
import { Button } from "@/components/ui/button"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { api, ApiError } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName } from "@/lib/format"
import { hud } from "@/lib/hud"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { cn } from "@/lib/utils"
import type { ThreadState } from "./model"
import { groupByPath, listDiffChanges } from "./diff-summary"

// Inspector rows are label left, value right: the pane is too narrow for the settings two-column form.
function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-(--s4)">
      <dt className="shrink-0 text-(--fg2)">{label}</dt>
      <dd className="min-w-0 truncate text-right" title={typeof children === "string" ? children : undefined}>{children}</dd>
    </div>
  )
}

function tokens(n: number): string {
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n)
}

export function ConversationInspector({
  thread,
  info,
  machine,
  machineName,
  open,
  sessionId,
  readOnly,
}: {
  thread: ThreadState
  info?: SessionInfo
  machine: string
  machineName?: string
  open: boolean
  sessionId?: string
  readOnly?: boolean
}) {
  const actor = info?.started_by
  const startedBy = actor ? [actor.user ?? actor.node, actor.kind].filter(Boolean).join(" · ") : undefined
  const capabilities = info?.agent_capabilities
  const imageSupport = capabilities?.promptCapabilities?.image
  const sessionCapabilities = capabilities?.sessionCapabilities
  const resumeSupport =
    capabilities?.loadSession === true ||
    (sessionCapabilities?.resume !== undefined && sessionCapabilities.resume !== null)
  const forkSupport =
    sessionCapabilities?.fork !== undefined && sessionCapabilities.fork !== null
  const all = listDiffChanges(thread.items)
  const [scope, setScope] = useState<"turn" | "all">("turn")
  const [confirming, setConfirming] = useState(false)
  const [revertError, setRevertError] = useState<string>()
  const lastTurn = Math.max(0, ...all.map((c) => c.turn))
  const changes = groupByPath(scope === "turn" ? all.filter((c) => c.turn === lastTurn) : all)
  return (
    <aside
      data-slot="conversation-inspector"
      aria-label="Session details"
      hidden={!open}
      className={cn(
        "shrink-0 flex-col overflow-auto border-l-[0.5px] border-(--sep) bg-(--sidebar) w-(--inspector-w) gap-(--s6) p-(--s5)",
        open ? "flex" : "hidden",
        open && "max-[1040px]:absolute max-[1040px]:inset-y-0 max-[1040px]:right-0 max-[1040px]:z-30 max-[1040px]:max-w-[calc(100vw-64px)]",
      )}
      style={{ ["--inspector-w" as string]: "272px" }}
    >
      <div id="conversation-inspector-content" className="flex min-w-0 flex-col gap-(--s6) text-(length:--fs-sm)">
        {thread.context_limit !== undefined && thread.context_tokens !== undefined && (
          <Section title="Context">
            <div role="meter" aria-label="Context used" aria-valuemin={0} aria-valuemax={thread.context_limit} aria-valuenow={thread.context_tokens} className="mb-(--s4) h-1.5 overflow-hidden rounded-full bg-(--track)">
              <div className="h-full rounded-full bg-(--accent)" style={{ width: `${Math.min(100, (thread.context_tokens / Math.max(thread.context_limit, 1)) * 100)}%` }} />
            </div>
            <dl className="flex flex-col gap-(--s2)">
              <Row label="Used">{`${tokens(thread.context_tokens)} of ${tokens(thread.context_limit)}`}</Row>
              {thread.cost_usd !== undefined && thread.cost_usd > 0 && <Row label="Cost so far">{`$${thread.cost_usd.toFixed(2)}`}</Row>}
            </dl>
          </Section>
        )}
        {all.length > 0 && (
          <Section title="Changes">
            <SegmentedControl aria-label="Changes scope" value={[scope]} onValueChange={(v) => v[0] && setScope(v[0] as "turn" | "all")}>
              <SegmentedControlItem value="turn">{lastTurn > 0 ? `Turn ${lastTurn}` : "Latest"}</SegmentedControlItem>
              <SegmentedControlItem value="all">All</SegmentedControlItem>
            </SegmentedControl>
            <ul className="flex flex-col gap-(--s2)">
              {changes.map((change) => {
                return (
                  <li key={change.path}>
                    <button
                      type="button"
                      className="flex w-full items-center gap-(--s2) rounded-(--r-row) border-[0.5px] border-(--sep) bg-(--window) px-(--s3) py-(--s2) text-left text-(length:--fs-sm) hover:bg-(--hover)"
                      onClick={() => {
                        const target = document.getElementById(change.anchor)
                        if (target) {
                          target.scrollIntoView({ behavior: "smooth", block: "center" })
                        } else {
                          hud.error("Couldn't find that diff in the thread.")
                        }
                      }}
                    >
                      <FileCode2 aria-hidden className="size-(--s4) shrink-0 text-(--fg2)" />
                      <code className="min-w-0 flex-1 break-all font-(family-name:--mono) text-(length:--fs-xs)">{change.path}</code>
                      <span className="shrink-0 tabular-nums text-(--ok-word)">+{change.add}</span>
                      <span className="shrink-0 tabular-nums text-(--danger-word)">−{change.del}</span>
                    </button>
                  </li>
                )
              })}
            </ul>
            {scope === "turn" && lastTurn > 0 && sessionId && (
              <>
                <Button type="button" size="sm" variant="ghost" disabled={readOnly} onClick={() => setConfirming(true)}>
                  Revert turn {lastTurn}
                </Button>
                {revertError && <p role="alert" className="text-(length:--fs-sm) text-(--danger-word)">{revertError}</p>}
                <ConfirmAlert open={confirming} onOpenChange={setConfirming}>
                  {confirming && (
                    <ConfirmAlertContent
                      title={`Revert turn ${lastTurn}?`}
                      description="Restores the files this turn changed. Refused if any of them changed since."
                      destructiveLabel="Revert"
                      onConfirm={() => {
                        setConfirming(false)
                        setRevertError(undefined)
                        api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/revert`, { machine, method: "POST", body: { turn: lastTurn } }).then(
                          () => hud.success(`Reverted turn ${lastTurn}`),
                          (error: unknown) =>
                            setRevertError(error instanceof ApiError && error.status === 404 ? "Needs update" : error instanceof Error ? error.message : "Couldn't revert this turn."),
                        )
                      }}
                    />
                  )}
                </ConfirmAlert>
              </>
            )}
          </Section>
        )}
        {info && (
          <Section title="Session">
            <dl className="flex flex-col gap-(--s2)">
              <Row label="Agent">{harnessName(info.harness)}</Row>
              {startedBy && <Row label="Started by">{startedBy}</Row>}
              <Row label="Machine">{machineName ?? machine}</Row>
              <Row label="Project">{info.directory.split("/").pop() || info.directory}</Row>
              <Row label="Mode">{info.mode || "default"}</Row>
            </dl>
          </Section>
        )}
        {capabilities && (
          <Section title="This agent supports">
            <dl className="flex flex-col gap-(--s2)">
              <Row label="Images">{imageSupport === true ? "Yes" : "No"}</Row>
              <Row label="Resume">{resumeSupport ? "Yes" : "No"}</Row>
              <Row label="Fork">{forkSupport ? "Yes" : "No"}</Row>
            </dl>
          </Section>
        )}
      </div>
    </aside>
  )
}
