import type { GoalState } from "./api-types"

// Active time: the stored total plus the current active stretch.
export function goalElapsedMs(goal: GoalState, now: number = Date.now()): number {
  const running = goal.state === "active" && goal.resumed_at ? Math.max(0, now - goal.resumed_at) : 0
  return (goal.active_ms ?? 0) + running
}

// "45s", "12m 05s", "1h 30m".
export function clock(ms: number): string {
  const seconds = Math.floor(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`
}

export const goalStateWords: Record<GoalState["state"], string> = {
  active: "Pursuing goal",
  paused: "Goal paused",
  blocked: "Goal blocked",
  limited: "Goal limit reached",
  met: "Goal met",
  cleared: "Goal cleared",
}

// A goal still worth showing in the sidebar and status bar.
export function liveGoal(goal: GoalState | undefined | null): goal is GoalState {
  return !!goal && goal.state !== "cleared"
}
