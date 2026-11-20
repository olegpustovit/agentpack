import { useState, type CSSProperties, type ReactNode } from "react"
import { ArrowUpRight, FileCode2 } from "lucide-react"
import { Section } from "@/components/kit/data"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { useSessionCommands } from "@/features/sessions/actions"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { api, ApiError } from "@/lib/api"
import type { GoalState, SessionInfo } from "@/lib/api-types"
import { clock, goalElapsedMs, goalStateWords } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import { harnessName } from "@/lib/format"
import { hud } from "@/lib/hud"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { cn } from "@/lib/utils"
import type { ThreadState } from "./model"
import type { GoalAction } from "./goal-sheet"
import { subagentRunning, subagentStats } from "./subagent-card"
import { fileLabel, groupByPath, listDiffChanges, reviewPrompt, type ReviewComment } from "./diff-summary"
import { ToolDiffBody } from "./tool-diff"
import { projectDirectory } from "@/features/sessions/git"

type Draft = ReviewComment & { key: string }

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
  goal,
  onGoalEdit,
  onGoalAction,
  compact,
  compactReason,
  onOpenSubagent,
  onSendComments,
  sendingComments,
  git,
}: {
  thread: ThreadState
  info?: SessionInfo
  machine: string
  machineName?: string
  open: boolean
  sessionId?: string
  readOnly?: boolean
  goal?: GoalState
  onGoalEdit?: () => void
  onGoalAction?: (action: GoalAction) => void
  /** Sends the agent's /compact; absent when the agent doesn't offer it. */
  compact?: () => void
  compactReason?: string
  onOpenSubagent?: (id: string) => void
  onSendComments?: (text: string, sent: () => void) => void
  sendingComments?: boolean
  /** Worktree and Pull request sections, shown above Session. */
  git?: ReactNode
}) {
  const subagents = Object.values(thread.subagents ?? {}).sort((a, b) => (b.startedTs ?? 0) - (a.startedTs ?? 0))
  const now = useNow(goal?.state === "active" || subagents.some(subagentRunning))
  const actor = info?.started_by
  const startedBy = actor?.agent ? harnessName(actor.agent) : actor ? [actor.user ?? actor.node, actor.kind].filter(Boolean).join(" · ") : undefined
  const commands = useSessionCommands()
  // Shown at once; the list refetch then confirms it.
  const [tools, setTools] = useState<boolean>()
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
  const scoped = scope === "turn" ? all.filter((c) => c.turn === lastTurn) : all
  const changes = groupByPath(scoped)
  // The selected file's edits in scope; switching scope away from it closes the review.
  const [selected, setSelected] = useState<string>()
  const selectedBlocks = scoped.filter((c) => c.path === selected).map((c) => c.block)
  const reviewing = selectedBlocks.length > 0
  // Drafts are pane state: the pane is keyed by session, so they never leak between sessions.
  const [comments, setComments] = useState<Draft[]>([])
  const [editing, setEditing] = useState<Draft>()
  const agentName = info ? harnessName(info.harness) : "the agent"
  const canSendComments = Boolean(onSendComments) && comments.length > 0 && !readOnly && !thread.needs_you && !sendingComments
  function saveDraft() {
    if (!editing) return
    const rest = comments.filter((c) => c.key !== editing.key)
    setComments(editing.text.trim() ? [...rest, editing] : rest)
    setEditing(undefined)
  }
  function draftFor(key: string) {
    if (editing?.key === key) {
      return (
        <div data-slot="diff-comment-draft" className="mx-(--s2) my-(--s2) flex flex-col gap-(--s2) rounded-(--r-pop) border-[0.5px] border-(--sep) bg-(--window) p-(--s3)">
          <textarea
            aria-label={`Comment on ${editing.path}:${editing.line}`}
            rows={2}
            autoFocus
            value={editing.text}
            className="min-h-12 w-full resize-y rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--control) px-(--s3) py-(--s2) text-(length:--fs-sm) text-(--fg) outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            onChange={(event) => setEditing({ ...editing, text: event.currentTarget.value })}
            onKeyDown={(event) => {
              if (event.key === "Escape") setEditing(undefined)
              if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                event.preventDefault()
                saveDraft()
              }
            }}
          />
          <div className="flex items-center justify-end gap-(--s2)">
            <span className="mr-auto text-(length:--fs-xs) text-(--fg2)">Comments go to {agentName} together.</span>
            <Button type="button" size="sm" onClick={() => setEditing(undefined)}>
              Cancel
            </Button>
            <Button type="button" size="sm" variant="primary" disabled={!editing.text.trim()} onClick={saveDraft}>
              Add comment
            </Button>
          </div>
        </div>
      )
    }
    const saved = comments.find((c) => c.key === key)
    if (!saved) return undefined
    return (
      <div data-slot="diff-comment" className="mx-(--s2) my-(--s1) flex items-start gap-(--s2) rounded-(--r-pop) border-[0.5px] border-(--sep) bg-(--window) px-(--s3) py-(--s2) text-(length:--fs-sm) text-(--fg)">
        <p className="min-w-0 flex-1 whitespace-pre-wrap break-words">{saved.text}</p>
        <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(saved)}>
          Edit
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={() => setComments(comments.filter((c) => c.key !== key))}>
          Remove
        </Button>
      </div>
    )
  }
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
      style={{ "--inspector-w": reviewing ? "480px" : "272px" } as CSSProperties}
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
            <Button type="button" size="sm" className="mt-(--s3)" disabled={readOnly || !compact} title={compact ? "Summarise the conversation to free context" : compactReason} onClick={compact}>
              Compact
            </Button>
          </Section>
        )}
        {goal && (
          <Section title="Goal">
            <p className="mb-(--s3) whitespace-pre-wrap text-(--fg) [overflow-wrap:anywhere]">{goal.objective}</p>
            <dl className="flex flex-col gap-(--s2)">
              <Row label="State">{goalStateWords[goal.state]}</Row>
              <Row label="Time">{clock(goalElapsedMs(goal, now))}</Row>
              {goal.turns !== undefined && <Row label="Turns">{String(goal.turns)}</Row>}
              {goal.checks && <Row label="Done when">{goal.checks}</Row>}
              {goal.last_reason && <Row label="Last check">{goal.last_reason}</Row>}
              {goal.limits && Object.entries(goal.limits).filter(([, value]) => value).map(([key, value]) => (
                <Row key={key} label={`Limit: ${key === "cost_usd" ? "cost" : key}`}>{key === "cost_usd" ? `$${value}` : String(value)}</Row>
              ))}
            </dl>
            {!readOnly && (
              <div className="mt-(--s3) flex flex-wrap gap-(--s2)">
                {goal.state === "active" ? (
                  <Button type="button" size="sm" onClick={() => onGoalAction?.("pause")}>Pause</Button>
                ) : goal.state !== "met" && (
                  <Button type="button" size="sm" onClick={() => onGoalAction?.("resume")}>Resume</Button>
                )}
                <Button type="button" size="sm" onClick={onGoalEdit}>Edit</Button>
                <Button type="button" size="sm" onClick={() => onGoalAction?.("clear")}>Clear</Button>
              </div>
            )}
          </Section>
        )}
        {subagents.length > 0 && (
          <Section title="Subagents">
            <ul className="flex flex-col gap-(--s1)">
              {subagents.map((subagent) => (
                <li key={subagent.id}>
                  <button
                    type="button"
                    className="flex w-full items-center gap-(--s2) rounded-(--r-row) px-(--s2) py-(--s1) text-left hover:bg-(--hover)"
                    onClick={() => onOpenSubagent?.(subagent.id)}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block truncate">{subagent.title || "Subagent"}</span>
                      <span className="block truncate text-(length:--fs-xs) tabular-nums text-(--fg3)">
                        {subagentRunning(subagent) ? (subagent.state === "requires_action" ? "Needs you · " : "Running · ") : ""}{subagentStats(subagent, now)}
                      </span>
                    </span>
                    <ArrowUpRight aria-hidden className="size-3.5 shrink-0 text-(--fg3)" />
                  </button>
                </li>
              ))}
            </ul>
          </Section>
        )}
        {all.length > 0 && (
          <Section title="Changes">
            <div className="flex flex-col gap-(--s3)">
              <SegmentedControl aria-label="Changes scope" value={[scope]} onValueChange={(v) => v[0] && setScope(v[0] as "turn" | "all")}>
                <SegmentedControlItem value="turn">{lastTurn > 0 ? `Turn ${lastTurn}` : "Latest"}</SegmentedControlItem>
                <SegmentedControlItem value="all">All</SegmentedControlItem>
              </SegmentedControl>
              <ul className="-mx-(--s2) flex flex-col">
                {changes.map((change) => {
                  const { name, folder } = fileLabel(change.path, info?.directory)
                  return (
                    <li key={change.path}>
                      <button
                        type="button"
                        title={change.path}
                        aria-pressed={reviewing && selected === change.path}
                        className="flex w-full min-w-0 items-center gap-(--s2) rounded-(--r-row) px-(--s2) py-(--s1) text-left hover:bg-(--hover) aria-pressed:bg-(--sel-inactive)"
                        onClick={() => {
                          if (reviewing && selected === change.path) {
                            setSelected(undefined)
                            return
                          }
                          setSelected(change.path)
                          setEditing(undefined)
                          const target = document.getElementById(change.anchor)
                          if (target) {
                            target.scrollIntoView({ behavior: "smooth", block: "center" })
                          } else {
                            hud.error("Couldn't find that diff in the thread.")
                          }
                        }}
                      >
                        <FileCode2 aria-hidden className="size-(--s4) shrink-0 text-(--fg3)" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate">{name}</span>
                          {folder && <span className="block truncate text-(length:--fs-xs) text-(--fg3)">{folder}</span>}
                        </span>
                        <span className="shrink-0 text-(length:--fs-xs) tabular-nums">
                          <span className="text-(--ok-word)">+{change.add}</span> <span className="text-(--danger-word)">−{change.del}</span>
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ul>
              {reviewing && (
                // shortcut: line numbers count from the start of each edit, because ACP diffs carry no file offset;
                // switch to file lines once the gateway records where each edit starts.
                <div data-slot="inspector-diff">
                  <ToolDiffBody
                    content={selectedBlocks}
                    // Line keys count blocks within this view, so they are qualified by scope and file.
                    review={{
                      onComment: (key, target) => {
                        const full = `${scope}:${selected}#${key}`
                        setEditing({ key: full, ...target, text: comments.find((c) => c.key === full)?.text ?? "" })
                      },
                      after: (key) => draftFor(`${scope}:${selected}#${key}`),
                    }}
                  />
                </div>
              )}
              {comments.length > 0 && (
                <div className="flex justify-end">
                  <Button
                    type="button"
                    size="sm"
                    variant="primary"
                    disabled={!canSendComments}
                    onClick={() => onSendComments?.(reviewPrompt(comments), () => setComments([]))}
                  >
                    Send {comments.length} {comments.length === 1 ? "comment" : "comments"} to {agentName}
                  </Button>
                </div>
              )}
              {scope === "turn" && lastTurn > 0 && sessionId && (
                <>
                  <Button type="button" size="sm" className="self-start" disabled={readOnly} onClick={() => setConfirming(true)}>
                    Revert turn {lastTurn}…
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
            </div>
          </Section>
        )}
        {git}
        {info && (
          <Section title="Session">
            <dl className="flex flex-col gap-(--s2)">
              <Row label="Agent">{harnessName(info.harness)}</Row>
              {startedBy && <Row label="Started by">{startedBy}</Row>}
              <Row label="Machine">{machineName ?? machine}</Row>
              <Row label="Project">{projectDirectory(info).split("/").pop() || info.directory}</Row>
              <Row label="Mode">{info.mode || "default"}</Row>
            </dl>
            {info.transport === "acp" && (
              <>
                <div className="mt-(--s3) flex items-center justify-between gap-(--s4)">
                  <label htmlFor="agentpack-tools" className="text-(--fg2)">agentpack tools</label>
                  <Switch
                    id="agentpack-tools"
                    aria-describedby="agentpack-tools-help"
                    checked={tools ?? Boolean(info.tools)}
                    disabled={readOnly || Boolean(info.archived_at) || tools !== undefined}
                    onCheckedChange={(next) => {
                      setTools(next)
                      void commands.setTools({ ...info, machine, machineName: machineName ?? machine, offline: false }, next).finally(() => setTools(undefined))
                    }}
                  />
                </div>
                <p id="agentpack-tools-help" className="mt-(--s1) text-(--fg3)">
                  Lets this agent see your other sessions and start helper sessions. It can never approve permissions.
                </p>
              </>
            )}
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
