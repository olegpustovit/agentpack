import { useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { ApiError, api, errorText } from "@/lib/api"
import { newIntentID, useIntent } from "@/lib/intent"
import { hud } from "@/lib/hud"
import { rowKey, sessionLabel, type ListedSession } from "./group"
import { sessionsQuery } from "./queries"
import { reusesOperationId, sessionRules } from "./rules"

export function useSessionCommands(onClosePending?: (session: ListedSession, pending: boolean) => void) {
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
  const reopen = (rows: ListedSession[]) => reopenRows(rows, refresh)
  const close = (rows: ListedSession[], name?: string) => closeRows(rows, name, refresh, onClosePending)

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
    close,
    reopen,
    setTools: async (session: ListedSession, enabled: boolean) => {
      await tools.mutateAsync({ session, enabled }).catch(() => undefined)
    },
  }
}

// Project settings and Install package open the project's page in the main pane; Install focuses its search.
export function projectPage(machine: string, directory: string, install = false) {
  return { to: "/sessions/project/$machine/$folder", params: { machine, folder: directory }, search: install ? { install: "1" } : {} } as const
}

const setClosed = (session: ListedSession, closed: boolean) =>
  api(`/api/sessions/${encodeURIComponent(session.id)}`, { machine: session.machine, method: "PATCH", body: { closed } })

function closeNeedsUpdate(error: unknown, session: ListedSession) {
  if (error instanceof ApiError && error.status === 400 && error.message === "name is required") {
    return `Update agentpack on ${session.machineName} to close sessions`
  }
}

async function reopenRows(rows: ListedSession[], refresh: () => Promise<unknown>) {
  const results = await Promise.allSettled(rows.map((row) => setClosed(row, false)))
  await refresh()
  const failed = rows.filter((_, index) => results[index]!.status === "rejected")
  if (failed.length) hud.error(`Couldn't reopen ${failed.length === 1 ? sessionLabel(failed[0]!) : `${failed.length} sessions`}`, () => void reopenRows(failed, refresh))
}

// Close stops the agent and takes the session out of the sidebar; it stays in History. Reopen undoes it.
// One outcome per gesture, so closing a project reads as one line with one Undo.
async function closeRows(rows: ListedSession[], name: string | undefined, refresh: () => Promise<unknown>, onPending?: (session: ListedSession, pending: boolean) => void) {
  const what = name ?? (rows.length === 1 ? `“${sessionLabel(rows[0]!)}”` : `${rows.length} sessions`)
  for (const row of rows) onPending?.(row, true)
  const results = await Promise.allSettled(rows.map(async (row) => {
    // The row may be stale. The close endpoint checks that the agent is stopped.
    if (sessionRules(row).stop) await api(`/api/sessions/${encodeURIComponent(row.id)}/stop`, { machine: row.machine, method: "POST" }).catch(() => undefined)
    await setClosed(row, true)
  }))
  await refresh()
  for (const row of rows) onPending?.(row, false)
  const done = rows.filter((_, index) => results[index]!.status === "fulfilled")
  const failed = rows.filter((_, index) => results[index]!.status === "rejected")
  if (done.length) hud.success(`Closed ${failed.length ? `${done.length} of ${rows.length} sessions` : what}`, () => void reopenRows(done, refresh))
  if (failed.length) {
    const reason = (results.find((result) => result.status === "rejected") as PromiseRejectedResult).reason
    hud.error(closeNeedsUpdate(reason, failed[0]!) ?? `Couldn't close ${failed.length === 1 ? sessionLabel(failed[0]!) : `${failed.length} sessions`}: ${errorText(reason)}`, () => void closeRows(failed, undefined, refresh, onPending))
  }
}
