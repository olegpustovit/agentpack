import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, ChevronRight, Circle, CircleDot, Flag, Pause, Pencil, Play, X } from "lucide-react"
import { Field, FieldError, FieldLabel } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import type { GoalLimits, GoalState } from "@/lib/api-types"
import { clock, goalElapsedMs, goalStateWords } from "@/lib/goal"
import { hud } from "@/lib/hud"
import { useNow } from "@/lib/use-now"
import { conversationErrorMessage, conversationNeedsUpdate } from "./queries"

export type GoalAction = "pause" | "resume" | "clear"

const goalPath = (sessionId: string): `/api/${string}` => `/api/sessions/${encodeURIComponent(sessionId)}/conversation/goal`

// Set, edit, pause, resume and clear. Each refreshes the thread, which records the change.
export function useGoalActions(machine: string, sessionId: string) {
  const queryClient = useQueryClient()
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["conversation", machine, sessionId] })
    void queryClient.invalidateQueries({ queryKey: ["m"] })
  }
  const set = useMutation({
    mutationFn: ({ goal, edit }: { goal: { objective: string; checks?: string; limits?: GoalLimits }; edit: boolean }) =>
      api(goalPath(sessionId), { machine, method: edit ? "PATCH" : "POST", body: goal }),
    onSuccess: refresh,
  })
  const act = useMutation({
    mutationFn: (action: GoalAction) =>
      action === "clear" ? api(goalPath(sessionId), { machine, method: "DELETE" }) : api(goalPath(sessionId), { machine, method: "PATCH", body: { action } }),
    onSuccess: refresh,
    onError: (error, action) =>
      hud.error(conversationErrorMessage(machine, `${action} the goal`, error), conversationNeedsUpdate(error) ? undefined : () => act.mutate(action)),
  })
  return { set, act }
}

// "/goal", "/goal pause", "/goal fix the flaky test": what the composer should do with it.
export function parseGoalCommand(text: string): { kind: "sheet" } | { kind: "action"; action: GoalAction } | { kind: "set"; objective: string } | undefined {
  const match = /^\/goal(?:\s+([\s\S]*))?$/.exec(text.trim())
  if (!match) return undefined
  const rest = (match[1] ?? "").trim()
  if (!rest) return { kind: "sheet" }
  if (rest === "pause" || rest === "resume" || rest === "clear") return { kind: "action", action: rest }
  return { kind: "set", objective: rest }
}

function limitNumber(value: string): number | undefined {
  const number = Number(value)
  return value.trim() && Number.isFinite(number) && number > 0 ? number : undefined
}

export function GoalSheet({
  open,
  onOpenChange,
  machine,
  sessionId,
  goal,
  initialObjective = "",
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  machine: string
  sessionId: string
  goal?: GoalState
  initialObjective?: string
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {open && <GoalForm machine={machine} sessionId={sessionId} goal={goal} initialObjective={initialObjective} onDone={() => onOpenChange(false)} />}
    </Sheet>
  )
}

function GoalForm({ machine, sessionId, goal, initialObjective, onDone }: { machine: string; sessionId: string; goal?: GoalState; initialObjective: string; onDone: () => void }) {
  const editing = Boolean(goal && goal.state !== "cleared" && goal.state !== "met")
  const [objective, setObjective] = useState(editing ? goal!.objective : initialObjective)
  const [checks, setChecks] = useState(editing ? goal!.checks ?? "" : "")
  const limits = editing ? goal!.limits : undefined
  const [minutes, setMinutes] = useState(limits?.minutes ? String(limits.minutes) : "")
  const [cost, setCost] = useState(limits?.cost_usd ? String(limits.cost_usd) : "")
  const [tokens, setTokens] = useState(limits?.tokens ? String(limits.tokens) : "")
  const [turns, setTurns] = useState(limits?.turns ? String(limits.turns) : "")
  const { set } = useGoalActions(machine, sessionId)
  const nextLimits: GoalLimits = { minutes: limitNumber(minutes), cost_usd: limitNumber(cost), tokens: limitNumber(tokens), turns: limitNumber(turns) }
  const anyLimit = Object.values(nextLimits).some((value) => value !== undefined)
  function submit() {
    if (!objective.trim()) return
    set.mutate(
      { goal: { objective: objective.trim(), checks: checks.trim() || undefined, limits: anyLimit ? nextLimits : undefined }, edit: editing },
      { onSuccess: onDone },
    )
  }
  return (
    <SheetContent>
      <SheetTitle>{editing ? "Edit goal" : "Set a goal"}</SheetTitle>
      <SheetDescription>The agent keeps working, turn after turn, until the goal is met. Pause or clear it any time.</SheetDescription>
      <form
        className="flex flex-col gap-(--s5)"
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <Field>
          <FieldLabel htmlFor="goal-objective">Objective</FieldLabel>
          <textarea
            id="goal-objective"
            autoFocus
            rows={3}
            value={objective}
            onChange={(event) => setObjective(event.currentTarget.value)}
            className="w-full resize-y rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--field) px-(--s3) py-(--s2) text-(length:--fs) outline-none focus:ring-2 focus:ring-(--accent-soft)"
          />
        </Field>
        <Field>
          <FieldLabel htmlFor="goal-checks">Done when</FieldLabel>
          <textarea
            id="goal-checks"
            rows={2}
            placeholder="go test ./... passes; the PR description is updated"
            value={checks}
            onChange={(event) => setChecks(event.currentTarget.value)}
            className="w-full resize-y rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--field) px-(--s3) py-(--s2) text-(length:--fs) outline-none placeholder:text-(--fg3) focus:ring-2 focus:ring-(--accent-soft)"
          />
        </Field>
        <details open={anyLimit} className="text-(length:--fs-sm)">
          <summary className="cursor-default text-(--fg2)">Optional limits</summary>
          <div className="mt-(--s3) grid grid-cols-2 gap-(--s3)">
            <Field>
              <FieldLabel htmlFor="goal-minutes">Minutes</FieldLabel>
              <Input id="goal-minutes" inputMode="numeric" value={minutes} onChange={(event) => setMinutes(event.currentTarget.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="goal-turns">Turns</FieldLabel>
              <Input id="goal-turns" inputMode="numeric" value={turns} onChange={(event) => setTurns(event.currentTarget.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="goal-cost">Cost (USD)</FieldLabel>
              <Input id="goal-cost" inputMode="decimal" value={cost} onChange={(event) => setCost(event.currentTarget.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="goal-tokens">Tokens</FieldLabel>
              <Input id="goal-tokens" inputMode="numeric" value={tokens} onChange={(event) => setTokens(event.currentTarget.value)} />
            </Field>
          </div>
        </details>
        {set.error && <FieldError>{conversationErrorMessage(machine, "set the goal", set.error)}</FieldError>}
        <SheetActions>
          <Button type="submit" variant="primary" disabled={!objective.trim() || set.isPending}>
            {set.isPending ? "Saving…" : editing ? "Save" : "Start"}
          </Button>
        </SheetActions>
      </form>
    </SheetContent>
  )
}

type PlanEntry = { id?: string; status?: string; content?: string }

// The goal takes the plan pill's slot and absorbs it: plan steps open from the pill.
export function GoalPill({
  goal,
  plan,
  readOnly,
  onEdit,
  onAction,
}: {
  goal?: GoalState
  plan: PlanEntry[]
  readOnly?: boolean
  onEdit: () => void
  onAction: (action: GoalAction) => void
}) {
  const now = useNow(goal?.state === "active")
  if (!goal && plan.length === 0) return null
  const done = plan.filter((entry) => entry.status === "completed").length
  const step = plan.find((entry) => entry.status === "in_progress") ?? plan.find((entry) => entry.status === "pending") ?? plan.at(-1)
  return (
    <details data-slot={goal ? "goal-pill" : "plan-pill"} className="group overflow-hidden rounded-(--r-sheet) bg-(--glass) shadow-(--shadow-composer) backdrop-blur-xl backdrop-saturate-[1.8]">
      <summary className="flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) px-(--s4) py-(--s2) text-(length:--fs-sm) marker:content-none [&::-webkit-details-marker]:hidden">
        {goal ? (
          <>
            <Flag aria-hidden className={`size-(--s4) shrink-0 ${goal.state === "active" ? "text-(--accent)" : goal.state === "met" ? "text-(--ok-word)" : "text-(--fg3)"}`} />
            <span className="shrink-0 font-medium">{goalStateWords[goal.state]}</span>
            <span className="shrink-0 tabular-nums text-(--fg2)">{clock(goalElapsedMs(goal, now))}</span>
            <span className="min-w-0 flex-1 truncate text-(--fg2)">{goal.objective}</span>
            {plan.length > 0 && <span className="shrink-0 text-(--fg3)">{done} of {plan.length}</span>}
          </>
        ) : (
          <>
            <span className="shrink-0 font-medium">{done} of {plan.length}</span>
            <span className="min-w-0 flex-1 truncate text-(--fg2)">{step?.content}</span>
          </>
        )}
        <ChevronRight aria-hidden className="size-(--s4) shrink-0 text-(--fg3) transition-transform group-open:rotate-90" />
      </summary>
      {goal && (
        <div className="flex flex-col gap-(--s2) border-t-[0.5px] border-(--sep) px-(--s4) py-(--s3) text-(length:--fs-sm)">
          {goal.checks && <p className="text-(--fg2)"><span className="text-(--fg3)">Done when: </span>{goal.checks}</p>}
          {goal.last_reason && <p className="text-(--fg2)"><span className="text-(--fg3)">Last check: </span>{goal.last_reason}</p>}
          {!readOnly && (
            <div className="flex flex-wrap gap-(--s2)">
              {goal.state === "active" ? (
                <Button type="button" size="sm" onClick={() => onAction("pause")}><Pause aria-hidden /> Pause</Button>
              ) : goal.state !== "met" ? (
                <Button type="button" size="sm" onClick={() => onAction("resume")}><Play aria-hidden /> Resume</Button>
              ) : null}
              <Button type="button" size="sm" onClick={onEdit}><Pencil aria-hidden /> Edit</Button>
              <Button type="button" size="sm" variant="ghost" onClick={() => onAction("clear")}><X aria-hidden /> Clear</Button>
            </div>
          )}
        </div>
      )}
      {plan.length > 0 && (
        <ol className="flex flex-col border-t-[0.5px] border-(--sep) px-(--s4) py-(--s2)">
          {plan.map((entry, index) => {
            const completed = entry.status === "completed"
            const active = entry.status === "in_progress"
            const label = completed ? "Completed" : active ? "In progress" : "Pending"
            return (
              <li
                key={entry.id ?? index}
                aria-label={`${label}: ${entry.content ?? "Plan step"}`}
                className={`flex min-h-(--row-h) items-center gap-(--s3) text-(length:--fs-sm) ${completed ? "text-(--fg3) line-through" : active ? "font-medium text-(--fg)" : "text-(--fg2)"}`}
              >
                <span className={`grid size-(--s4) shrink-0 place-items-center ${completed ? "text-(--ok-word)" : active ? "text-(--accent)" : "text-(--fg3)"}`}>
                  {completed ? <Check aria-hidden className="size-(--s4)" /> : active ? <CircleDot aria-hidden className="size-(--s4) animate-spin motion-reduce:animate-none" /> : <Circle aria-hidden className="size-(--s3)" />}
                </span>
                <span className="min-w-0">{entry.content}</span>
              </li>
            )
          })}
        </ol>
      )}
    </details>
  )
}
