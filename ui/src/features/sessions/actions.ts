import { useQueryClient } from "@tanstack/react-query"
import { useRef } from "react"
import { api } from "@/lib/api"
import { hud } from "@/lib/hud"
import { rowKey, sessionLabel, type ListedSession } from "./group"
import { sessionsQuery } from "./queries"
import { reusesOperationId } from "./rules"

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "the request did not complete"
}

// Stop and restart for session rows. A restart keeps one operation id per session until the
// gateway accepts it or rejects it (app.js:300-318), so a retry cannot start a second restart.
export function useSessionCommands() {
  const queryClient = useQueryClient()
  const operations = useRef(new Map<string, string>())

  async function refresh(session: ListedSession) {
    await queryClient.invalidateQueries({ queryKey: sessionsQuery(session.machine, false).queryKey })
  }

  async function stop(session: ListedSession): Promise<void> {
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { machine: session.machine, method: "POST" })
      hud.success("Agent stopped")
      await refresh(session)
    } catch (error) {
      hud.error(`Couldn't stop ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, () => void stop(session))
    }
  }

  async function restart(session: ListedSession): Promise<void> {
    const key = rowKey(session)
    // crypto.randomUUID does not exist on plain-HTTP gateway origins; getRandomValues does.
    const operationId = operations.current.get(key) ?? Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, "0")).join("")
    operations.current.set(key, operationId)
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/restart`, {
        machine: session.machine,
        method: "POST",
        body: { operation_id: operationId },
      })
      operations.current.delete(key)
      hud.success("Agent restart requested")
      await refresh(session)
    } catch (error) {
      if (!reusesOperationId(error)) operations.current.delete(key)
      hud.error(`Couldn't restart ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, () => void restart(session))
    }
  }

  return { stop, restart }
}
