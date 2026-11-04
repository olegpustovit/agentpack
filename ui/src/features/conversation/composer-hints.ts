export type ComposerHint = { value: string; description: string }

export const slashCommands = (): ComposerHint[] => [
  { value: "/compact", description: "Compact the conversation" },
  { value: "/fork", description: "Fork this session" },
  { value: "/model", description: "Switch model" },
  { value: "/mode", description: "Switch mode" },
  { value: "/review", description: "Review the current diff" },
  { value: "/terminal", description: "Reopen in the agent's own TUI" },
]

export function filterSlashHints(draft: string): ComposerHint[] {
  if (!draft.startsWith("/")) return []
  const query = draft.slice(1).toLowerCase()
  return slashCommands().filter((item) => item.value.slice(1).toLowerCase().startsWith(query))
}

export function filterAtHints(draft: string, paths: string[]): ComposerHint[] {
  const at = draft.lastIndexOf("@")
  if (at < 0) return []
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
