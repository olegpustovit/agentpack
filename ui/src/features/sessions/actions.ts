import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { api, routeMissing } from "@/lib/api"
import { newIntentID, useIntent } from "@/lib/intent"
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
  const queryClient = useQueryClient()
  const operations = useState(() => new Map<string, string>())[0]
  const { restart, refresh } = sessionCommands(queryClient, operations)

  const stop = useIntent({
    request: (session: ListedSession, id) => api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { machine: session.machine, method: "POST", idempotencyKey: id }),
    accepted: refresh,
    failed: (error, session, retry) => hud.error(`Couldn't stop ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, retry),
  })
  const resume = useIntent({
    request: (session: ListedSession, id) => api(`/api/sessions/${encodeURIComponent(session.id)}/resume`, { machine: session.machine, method: "POST", idempotencyKey: id, body: { text: "" } }),
    accepted: refresh,
    failed: (error, session, retry) => hud.error(`Couldn't resume ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, retry),
  })
  const archive = useIntent({
    request: async (session: ListedSession, id) => {
      const path: `/api/${string}` = `/api/sessions/${encodeURIComponent(session.id)}`
      const options = { machine: session.machine, idempotencyKey: id }
      // The row may be stale. The archive endpoint checks that the agent is stopped.
      if (session.state.running) await api(`${path}/stop`, { ...options, method: "POST" }).catch(() => undefined)
      return api(path, { ...options, method: "PATCH", body: { hidden: true } })
    },
    accepted: refresh,
    failed: (error, session, retry) => hud.error(`Couldn't archive ${sessionLabel(session)}: ${errorText(error)}`, retry),
  })
  const tools = useIntent({
    request: ({ session, enabled }: { session: ListedSession; enabled: boolean }) => api(`/api/sessions/${encodeURIComponent(session.id)}/tools`, { machine: session.machine, method: "POST", body: { enabled } }),
    accepted: refresh,
    failed: (error, { enabled }, retry) => hud.error(`Couldn't turn agentpack tools ${enabled ? "on" : "off"}: ${toolsError(error)}`, retry),
  })

  return {
    stop: (session: ListedSession) => stop.mutateAsync(session).then(() => true, () => false),
    restart,
    resume: resume.mutate,
    archive: (session: ListedSession) => archive.mutateAsync(session).then(() => true, () => false),
    setTools: async (session: ListedSession, enabled: boolean) => {
      await tools.mutateAsync({ session, enabled }).catch(() => undefined)
    },
  }
}

// Outside the hook: the retry callbacks reference their own function, which React Compiler cannot memoize.
function sessionCommands(queryClient: QueryClient, operations: Map<string, string>) {
  async function refresh() {
    await queryClient.invalidateQueries({ queryKey: sessionsQuery(false).queryKey })
  }

  async function restart(session: ListedSession): Promise<void> {
    const key = rowKey(session)
    const operationId = operations.get(key) ?? newIntentID()
    operations.set(key, operationId)
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/restart`, {
        machine: session.machine,
        method: "POST",
        body: { operation_id: operationId },
      })
      operations.delete(key)
      await refresh()
    } catch (error) {
      if (!reusesOperationId(error)) operations.delete(key)
      hud.error(`Couldn't restart ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, () => void restart(session))
    }
  }

  return { restart, refresh }
}
