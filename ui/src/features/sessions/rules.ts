import { ApiError } from "@/lib/api"
import type { ListedSession } from "./group"

// Row action availability. Non-interactive sessions have no browser terminal.
export function sessionRules(session: ListedSession) {
  const offline = session.offline
  const nonInteractive = session.state.interactive === false
  return {
    rename: !offline,
    restart: !offline && !nonInteractive,
    stop: !offline && session.state.running,
    reconnect: !offline && !nonInteractive,
    install: !offline,
    save: !offline,
  }
}

// A rejected request (4xx) drops the restart's operation id. A server error or a
// transport failure keeps it, so the retry is the same intent and the gateway can de-duplicate it.
export function reusesOperationId(error: unknown): boolean {
  return !(error instanceof ApiError && error.status >= 400 && error.status < 500)
}

export type SessionView = "chat" | "terminal"

export function startSessionBody(input: {
  directory: string
  harness: string
  mode: string
  view: SessionView
  agentSessionId?: string
}) {
  return {
    directory: input.directory,
    harness: input.harness,
    mode: input.mode.trim(),
    transport: input.view === "chat" ? "acp" : "terminal",
    ...(input.agentSessionId ? { agent_session_id: input.agentSessionId } : {}),
  }
}
