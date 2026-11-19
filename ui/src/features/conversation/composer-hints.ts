export type ComposerHint = { value: string; description: string; hint?: string; source?: "agent" | "gateway" }

// The agent advertises its own commands (skills, custom commands) over ACP; these are the gateway's.
const gatewayCommands: ComposerHint[] = [
  { value: "/goal", description: "Keep working until a goal is met", hint: "objective, or pause | resume | clear", source: "gateway" },
  { value: "/compact", description: "Summarise the conversation to free context", hint: "focus", source: "gateway" },
  { value: "/fork", description: "Fork this session", source: "gateway" },
  { value: "/terminal", description: "Reopen in the agent's own TUI", source: "gateway" },
]
// The gateway runs these itself, so the agent's own entry with the same name is not listed twice.
const gatewayOwned = new Set(["goal", "fork", "terminal"])

export type AgentCommand = { name?: string; description?: string; input?: { hint?: string } | null }

// The newest available_commands_update in the log is the agent's current list.
export function agentCommands(entries: { kind: string; payload: unknown }[]): AgentCommand[] | undefined {
  const update = entries.findLast((entry) => entry.kind === "available_commands_update")
  return (update?.payload as { availableCommands?: AgentCommand[] } | undefined)?.availableCommands
}

const commandsKey = (harness: string) => `agentpack.commands.${harness}`

// The last list each agent advertised, so the menu works before a chat's first update arrives.
export function cachedCommands(harness: string): AgentCommand[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(commandsKey(harness)) ?? "[]")
    return Array.isArray(value) ? (value as AgentCommand[]) : []
  } catch {
    return []
  }
}

export function rememberCommands(harness: string, commands: AgentCommand[]) {
  try {
    localStorage.setItem(commandsKey(harness), JSON.stringify(commands.slice(0, 200)))
  } catch {
    // The menu then waits for the agent's own update.
  }
}

export function advertises(commands: AgentCommand[], name: string): boolean {
  return commands.some((command) => command.name === name)
}

// The /word being typed at the start of the draft's last line, if any.
function slashQuery(draft: string): string | undefined {
  const line = draft.slice(draft.lastIndexOf("\n") + 1)
  return /^\/\S*$/.test(line) ? line.slice(1).toLowerCase() : undefined
}

export function filterSlashHints(draft: string, commands: AgentCommand[] = []): ComposerHint[] {
  const query = slashQuery(draft)
  if (query === undefined) return []
  const fromAgent = commands.flatMap((command) =>
    command.name && !gatewayOwned.has(command.name) && !(command.name === "compact")
      ? [{ value: `/${command.name}`, description: command.description ?? "", hint: command.input?.hint ?? undefined, source: "agent" as const }]
      : [],
  )
  return [...gatewayCommands, ...fromAgent].filter((item) => item.value.slice(1).toLowerCase().includes(query)).slice(0, 50)
}

export function filterAtHints(draft: string, paths: string[]): ComposerHint[] {
  const at = draft.lastIndexOf("@")
  if (at < 0 || (at > 0 && !/\s/.test(draft[at - 1]!))) return []
  const tail = draft.slice(at + 1)
  if (tail.includes(" ") || tail.includes("\n")) return []
  const query = tail.toLowerCase()
  return paths
    .filter((path) => path.toLowerCase().includes(query))
    .slice(0, 8)
    .map((path) => ({ value: `@${path}`, description: path }))
}

export function applyHint(draft: string, hint: ComposerHint): string {
  if (hint.value.startsWith("@")) {
    const at = draft.lastIndexOf("@")
    if (at < 0) return `${draft}${hint.value} `
    return `${draft.slice(0, at)}${hint.value} `
  }
  return `${draft.slice(0, draft.lastIndexOf("\n") + 1)}${hint.value} `
}

// Wraps around so ↑ on the first row lands on the last.
export function moveHint(index: number, delta: number, length: number): number {
  return length === 0 ? 0 : (((index + delta) % length) + length) % length
}
