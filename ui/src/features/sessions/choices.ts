import type { SessionView } from "./rules"

// Per-viewer conveniences: what New session last started in a folder, and the model and effort
// last chosen there for each agent. Storage may be blocked; everything falls back to nothing.
export type StartChoices = { harness?: string; mode?: string; view?: SessionView; workspace?: "folder" | "new" }
export type AgentChoices = Record<string, string | boolean>

const startKey = (directory: string) => `agentpack.newSession.${directory}`
const agentKey = (directory: string, harness: string) => `agentpack.agentChoices.${harness}.${directory}`
const PENDING = "agentpack.applyChoices"

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

export function loadAgentChoices(directory: string, harness: string): AgentChoices {
  const saved = read<AgentChoices>(agentKey(directory, harness)) ?? {}
  return Object.fromEntries(Object.entries(saved).filter(([, value]) => typeof value === "string" || typeof value === "boolean"))
}

export function rememberAgentChoice(directory: string, harness: string, key: string, value: string | boolean) {
  write(agentKey(directory, harness), { ...loadAgentChoices(directory, harness), [key]: value })
}

// A chat New session just started takes the folder's remembered model and effort once.
export function markNewChat(sessionId: string) {
  try {
    sessionStorage.setItem(`${PENDING}.${sessionId}`, "1")
  } catch {
    // The agent uses its own defaults.
  }
}

export function takeNewChat(sessionId: string): boolean {
  try {
    const pending = sessionStorage.getItem(`${PENDING}.${sessionId}`) !== null
    sessionStorage.removeItem(`${PENDING}.${sessionId}`)
    return pending
  } catch {
    return false
  }
}
