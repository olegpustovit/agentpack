import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { api, routeMissing } from "@/lib/api"
import { hud } from "@/lib/hud"
import { rowKey, sessionLabel, type ListedSession } from "./group"
import { sessionsQuery } from "./queries"
import { reusesOperationId } from "./rules"

// An older gateway has no switch.
export function toolsError(error: unknown): string {
  return routeMissing(error) ? "Needs update" : errorText(error)
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

  // Success shows in the row itself; only failures report.
  async function stop(session: ListedSession): Promise<boolean> {
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { machine: session.machine, method: "POST" })
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
      await refresh(session)
    } catch (error) {
      if (!reusesOperationId(error)) operations.delete(key)
      hud.error(`Couldn't restart ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, () => void restart(session))
    }
  }

  // A stopped chat starts its agent again; the row's state shows it coming up.
  async function resume(session: ListedSession): Promise<void> {
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/resume`, { machine: session.machine, method: "POST", body: { text: "" } })
      await refresh(session)
    } catch (error) {
      hud.error(`Couldn't resume ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, () => void resume(session))
    }
  }

  // Archive takes the session out of every list; a running agent is stopped first.
  async function archive(session: ListedSession): Promise<boolean> {
    try {
      // The row can be stale (just stopped): a failed stop is fine, the archive request refuses a live agent itself.
      if (session.state.running) await api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { machine: session.machine, method: "POST" }).catch(() => undefined)
      await api(`/api/sessions/${encodeURIComponent(session.id)}`, { machine: session.machine, method: "PATCH", body: { hidden: true } })
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
      await refresh(session)
    } catch (error) {
      hud.error(`Couldn't turn agentpack tools ${enabled ? "on" : "off"}: ${toolsError(error)}`, () => void setTools(session, enabled))
    }
  }

  return { stop, restart, resume, archive, setTools }
}
