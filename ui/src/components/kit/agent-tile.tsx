import { Asterisk, Bot, Box, Braces, MousePointer2, Orbit, Pi, Zap, type LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"

const agentIcons: Record<string, LucideIcon> = {
  claude: Asterisk,
  codex: Box,
  opencode: Braces,
  cursor: MousePointer2,
  agent: MousePointer2,
  grok: Zap,
  omp: Pi,
  agy: Orbit,
}

// The agent's icon with a corner status dot. One element whose data-state changes, so refetches
// never restart its pulse.
export function AgentTile({ harness, state, label, title, className, unread = false }: { harness: string; state: string; label: string; title?: string; className?: string; unread?: boolean }) {
  const Icon = agentIcons[harness] ?? Bot
  const dot = state === "needsYou" || state === "requires_action" ? "needs" : state === "running" ? "working" : state === "waiting" ? "waiting" : state === "restarting" ? "restarting" : state === "starting" ? "starting" : unread ? "done" : "none"
  return (
    <span data-slot="agent-tile" role="img" aria-label={label} title={title} className={cn("relative inline-grid size-[18px] shrink-0 place-items-center rounded-[5px] bg-(--track) text-(--fg2) sel:bg-(--accent-fg)/20 sel:text-(--accent-fg)", className)}>
      <Icon aria-hidden strokeWidth={1.75} className="size-3" />
      <span data-slot="agent-dot" data-state={dot} aria-hidden className="agent-dot" />
    </span>
  )
}
