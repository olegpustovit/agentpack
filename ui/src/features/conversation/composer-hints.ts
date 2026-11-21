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
  const update = entries.findLast((entry) => entry.kind === "available_commands_update" && !(entry.payload as { _subagent?: string })?._subagent)
  return (update?.payload as { availableCommands?: AgentCommand[] } | undefined)?.availableCommands
}

export function advertises(commands: AgentCommand[], name: string): boolean {
  return commands.some((command) => command.name === name)
}

// The /word ending at the caret (`before` is the draft up to it). The gateway runs its own commands
// only at the start of a line; the agent's commands also work mid-sentence.
export function slashQuery(before: string): { query: string; lineStart: boolean } | undefined {
  const match = /(^|\s)\/(\S*)$/.exec(before)
  if (!match) return undefined
  return { query: match[2]!.toLowerCase(), lineStart: match[1] === "" || match[1] === "\n" }
}

export function filterSlashHints(before: string, commands: AgentCommand[] = []): ComposerHint[] {
  const slash = slashQuery(before)
  if (!slash) return []
  const fromAgent = commands.flatMap((command) =>
    command.name && !gatewayOwned.has(command.name) && !(command.name === "compact")
      ? [{ value: `/${command.name}`, description: command.description ?? "", hint: command.input?.hint ?? undefined, source: "agent" as const }]
      : [],
  )
  return [...(slash.lineStart ? gatewayCommands : []), ...fromAgent].filter((item) => item.value.slice(1).toLowerCase().includes(slash.query)).slice(0, 50)
}

const gatewayNames = new Set(gatewayCommands.map((command) => command.value.slice(1)))

// Splits the draft so the composer can mark every command it would run, by the same rules as the hints.
export function commandSpans(draft: string, commands: AgentCommand[]): { text: string; command: boolean }[] {
  const agent = new Set(commands.flatMap((command) => (command.name ? [command.name] : [])))
  const spans: { text: string; command: boolean }[] = []
  let last = 0
  for (const match of draft.matchAll(/(^|\s)\/(\S+)/g)) {
    const name = match[2]!
    const lineStart = match[1] === "" || match[1] === "\n"
    if (!agent.has(name) && !(lineStart && gatewayNames.has(name))) continue
    const start = match.index + match[1]!.length
    if (start > last) spans.push({ text: draft.slice(last, start), command: false })
    spans.push({ text: `/${name}`, command: true })
    last = start + name.length + 1
  }
  if (last < draft.length) spans.push({ text: draft.slice(last), command: false })
  return spans
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

// Replaces the fragment ending at the caret; `before` is the draft up to it.
export function applyHint(before: string, hint: ComposerHint): string {
  if (hint.value.startsWith("@")) {
    const at = before.lastIndexOf("@")
    if (at < 0) return `${before}${hint.value} `
    return `${before.slice(0, at)}${hint.value} `
  }
  return before.replace(/\/\S*$/, `${hint.value} `)
}

// Wraps around so ↑ on the first row lands on the last.
export function moveHint(index: number, delta: number, length: number): number {
  return length === 0 ? 0 : (((index + delta) % length) + length) % length
}
