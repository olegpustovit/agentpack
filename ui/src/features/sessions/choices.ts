import type { SessionView } from "./rules"

// Per-viewer conveniences for the New Session form.
export type StartChoices = { harness?: string; mode?: string; view?: SessionView; workspace?: "folder" | "new" }

const startKey = (directory: string) => `agentpack.newSession.${directory}`

function read<T>(key: string): T | undefined {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) ?? "null")
    return value && typeof value === "object" ? (value as T) : undefined
  } catch {
    return undefined
  }
}

function write(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // The sheet then falls back to the folder's newest launch.
  }
}

export function loadStartChoices(directory: string): StartChoices {
  const saved = read<StartChoices>(startKey(directory)) ?? {}
  return {
    harness: typeof saved.harness === "string" ? saved.harness : undefined,
    mode: typeof saved.mode === "string" ? saved.mode : undefined,
    view: saved.view === "chat" || saved.view === "terminal" ? saved.view : undefined,
    workspace: saved.workspace === "folder" || saved.workspace === "new" ? saved.workspace : undefined,
  }
}

export function saveStartChoices(directory: string, choices: StartChoices) {
  write(startKey(directory), choices)
}
