export type ComposerHint = { value: string; description: string }

// The agent advertises its own commands (skills, custom commands) over ACP; /fork and /terminal are the gateway's.
const localCommands: ComposerHint[] = [
  { value: "/fork", description: "Fork this session" },
  { value: "/terminal", description: "Reopen in the agent's own TUI" },
]

export type AgentCommand = { name?: string; description?: string }

// The newest available_commands_update in the log is the agent's current list.
export function agentCommands(entries: { kind: string; payload: unknown }[]): AgentCommand[] {
  const update = entries.findLast((entry) => entry.kind === "available_commands_update")
  return (update?.payload as { availableCommands?: AgentCommand[] } | undefined)?.availableCommands ?? []
}

export function filterSlashHints(draft: string, commands: AgentCommand[] = []): ComposerHint[] {
  if (!draft.startsWith("/") || /\s/.test(draft)) return []
  const query = draft.slice(1).toLowerCase()
  const fromAgent = commands.flatMap((command) => command.name ? [{ value: `/${command.name}`, description: command.description ?? "" }] : [])
  return [...fromAgent, ...localCommands].filter((item) => item.value.slice(1).toLowerCase().includes(query)).slice(0, 50)
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
  return `${hint.value} `
}

// Wraps around so ↑ on the first row lands on the last.
export function moveHint(index: number, delta: number, length: number): number {
  return length === 0 ? 0 : (((index + delta) % length) + length) % length
}
