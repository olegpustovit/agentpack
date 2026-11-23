import { useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { api, errorText } from "@/lib/api"
import { newIntentID, useIntent } from "@/lib/intent"
import { hud } from "@/lib/hud"
import { rowKey, sessionLabel, type ListedSession } from "./group"
import { sessionsQuery } from "./queries"
import { reusesOperationId } from "./rules"

export function useSessionCommands(onArchivePending?: (session: ListedSession, pending: boolean) => void) {
  const queryClient = useQueryClient()
  const operations = useState(() => new Map<string, string>())[0]
  const refresh = () => queryClient.invalidateQueries({ queryKey: sessionsQuery(false).queryKey })

  const stop = useIntent({
    request: (session: ListedSession, id) => api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { machine: session.machine, method: "POST", idempotencyKey: id }),
    accepted: refresh,
    failed: (error, session, retry) => hud.error(`Couldn't stop ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, retry),
  })
  const restart = useIntent({
    request: async (session: ListedSession, id) => {
      const key = rowKey(session)
      if (!operations.has(key)) operations.set(key, id)
      try {
        const result = await api(`/api/sessions/${encodeURIComponent(session.id)}/restart`, {
          machine: session.machine,
          method: "POST",
          body: { operation_id: id },
        })
        if (operations.get(key) === id) operations.delete(key)
        return result
      } catch (error) {
        if (!reusesOperationId(error) && operations.get(key) === id) operations.delete(key)
        throw error
      }
    },
    accepted: refresh,
    retryID: (error, id) => reusesOperationId(error) ? id : newIntentID(),
    failed: (error, session, retry) => hud.error(`Couldn't restart ${sessionLabel(session)} on ${session.machineName}: ${errorText(error)}`, retry),
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
    apply: (session) => {
      onArchivePending?.(session, true)
      return () => onArchivePending?.(session, false)
    },
    accepted: (_result, session) => refresh().finally(() => onArchivePending?.(session, false)),
    failed: (error, session, retry) => hud.error(`Couldn't archive ${sessionLabel(session)}: ${errorText(error)}`, retry),
  })
  const tools = useIntent({
    request: ({ session, enabled }: { session: ListedSession; enabled: boolean }) => api(`/api/sessions/${encodeURIComponent(session.id)}/tools`, { machine: session.machine, method: "POST", body: { enabled } }),
    accepted: refresh,
    failed: (error, { enabled }, retry) => hud.error(`Couldn't turn agentpack tools ${enabled ? "on" : "off"}: ${errorText(error)}`, retry),
  })

  return {
    stop: (session: ListedSession) => stop.mutateAsync(session).then(() => true, () => false),
    restart: (session: ListedSession) => {
      // A second click after an ambiguous response must keep the pending operation ID.
      const key = rowKey(session)
      const id = operations.get(key) ?? newIntentID()
      operations.set(key, id)
      restart.mutate(session, id)
    },
    resume: resume.mutate,
    archive: archive.mutate,
    setTools: async (session: ListedSession, enabled: boolean) => {
      await tools.mutateAsync({ session, enabled }).catch(() => undefined)
    },
  }
}
