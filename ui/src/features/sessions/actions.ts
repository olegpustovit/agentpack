import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { api, ApiError } from "@/lib/api"
import { hud } from "@/lib/hud"
import { rowKey, sessionLabel, type ListedSession } from "./group"
import { sessionsQuery } from "./queries"
import { reusesOperationId } from "./rules"
import type { SessionInfo } from "@/lib/api-types"

// An older gateway has no switch.
export function toolsError(error: unknown): string {
  return error instanceof ApiError && error.status === 404 ? "Needs update" : errorText(error)
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "the request did not complete"
}

// Stop and restart for session rows. A restart keeps one operation id per session until the
// gateway accepts it or rejects it, so a retry cannot start a second restart.
export function useSessionCommands() {
  return sessionCommands(useQueryClient(), useState(() => new Map<string, string>())[0])
}

// Outside the hook: the retry callbacks reference their own function, which React Compiler cannot memoize.
function sessionCommands(queryClient: QueryClient, operations: Map<string, string>) {

  async function refresh(session: ListedSession) {
    await queryClient.invalidateQueries({ queryKey: sessionsQuery(session.machine, false).queryKey })
  }

  // `announce` is off for multi-row actions, which show one Hud for the batch. Failures always report.
  async function stop(session: ListedSession, announce = true): Promise<boolean> {
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { machine: session.machine, method: "POST" })
      if (announce) hud.success("Agent stopped")
      await refresh(session)
      return true
    } catch (error) {
      hud.error(`Couldn't stop ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, () => void stop(session))
      return false
    }
  }

  async function restart(session: ListedSession): Promise<void> {
    const key = rowKey(session)
    // crypto.randomUUID does not exist on plain-HTTP gateway origins; getRandomValues does.
    const operationId = operations.get(key) ?? Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, "0")).join("")
    operations.set(key, operationId)
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/restart`, {
        machine: session.machine,
        method: "POST",
        body: { operation_id: operationId },
      })
      operations.delete(key)
      hud.success("Agent restart requested")
      await refresh(session)
    } catch (error) {
      if (!reusesOperationId(error)) operations.delete(key)
      hud.error(`Couldn't restart ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, () => void restart(session))
    }
  }

  // Archive takes the session out of every list; a running agent is stopped first.
  async function archive(session: ListedSession, announce = true): Promise<boolean> {
    try {
      // The row can be stale (just stopped): a failed stop is fine, the archive request refuses a live agent itself.
      if (session.state.running) await api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { machine: session.machine, method: "POST" }).catch(() => undefined)
      await api(`/api/sessions/${encodeURIComponent(session.id)}`, { machine: session.machine, method: "PATCH", body: { hidden: true } })
      if (announce) hud.success("Session archived")
      await refresh(session)
      return true
    } catch (error) {
      hud.error(`Couldn't archive ${sessionLabel(session)}: ${errorText(error)}`, () => void archive(session))
      return false
    }
  }

  // agentpack tools for a chat's agent; while off, the gateway refuses its next call.
  async function setTools(session: ListedSession, enabled: boolean): Promise<void> {
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/tools`, { machine: session.machine, method: "POST", body: { enabled } })
      hud.success(enabled ? "agentpack tools on" : "agentpack tools off")
      await refresh(session)
    } catch (error) {
      hud.error(`Couldn't turn agentpack tools ${enabled ? "on" : "off"}: ${toolsError(error)}`, () => void setTools(session, enabled))
    }
  }

  return { stop, restart, archive, setTools }
}

// The message that resumed a stopped chat; the new chat sends it once it runs.
let carried: { id: string; text: string } | undefined

// Resumes a stopped chat's agent conversation in a new chat that sends text first; undefined if it failed.
export async function resumeChat(queryClient: QueryClient, machine: string, info: SessionInfo, text: string): Promise<string | undefined> {
  try {
    const next = await api<SessionInfo>("/api/sessions", {
      machine,
      method: "POST",
      body: { directory: info.directory, harness: info.harness, mode: info.mode, transport: "acp", agent_session_id: info.agent_session_id },
    })
    carried = { id: next.id, text }
    await queryClient.invalidateQueries({ queryKey: sessionsQuery(machine, false).queryKey })
    return next.id
  } catch (error) {
    hud.error(`Couldn't resume the chat: ${errorText(error)}`)
    return undefined
  }
}

export function takeCarried(id: string): string | undefined {
  const text = carried?.id === id ? carried.text : undefined
  if (text !== undefined) carried = undefined
  return text
}
