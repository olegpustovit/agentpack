import { useState } from "react"
import { Field, FieldError, FieldLabel } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { GoalLimits, GoalState } from "@/lib/api-types"
import { conversationErrorMessage } from "./queries"
import { useGoalActions } from "./goal-sheet"

// The goal editor loads when it first opens; the pill and actions stay in goal-sheet.
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
