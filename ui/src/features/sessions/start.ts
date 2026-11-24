import { useQueryClient } from "@tanstack/react-query"
import { useNavigate, useRouter } from "@tanstack/react-router"
import { api, ApiError, LOCAL } from "@/lib/api"
import { useIntent } from "@/lib/intent"
import { hud } from "@/lib/hud"
import type { SessionInfo } from "@/lib/api-types"
import { saveStartChoices } from "./choices"
import { sessionRowQuery, sessionsQuery } from "./queries"
import { reusesOperationId, startSessionBody, type SessionView } from "./rules"
import { onSignOut } from "@/lib/persist"
import { search } from "@/lib/search"

// `prompt` is the new chat's first queued message.
// `worktree` is "new", an existing worktree of directory, or "" for the folder itself (remembered as the
// choice); a new worktree's branch is named from the prompt.
export type StartInput = { machine: string; directory: string; harness: string; mode: string; view: SessionView; agentSessionId?: string; prompt?: string; sendOnResume?: boolean; worktree?: string; carry?: boolean; forkFrom?: string; switchFrom?: string }

const chatClaims = new Map<string, Promise<SessionInfo>>()
const sessionSearch = search({ q: "text", group: "text", tab: "text" })
onSignOut(() => chatClaims.clear())

export function pendingChatClaim(id: string): Promise<SessionInfo> | undefined {
  return chatClaims.get(id)
}

// The reviewed form stays put until the gateway accepts. The quick "+" and fork open a provisional chat.
export function useStartSession(review?: { onAccepted?: (input: StartInput) => void }) {
  const navigate = useNavigate()
  const router = useRouter()
  const queryClient = useQueryClient()
  const key = sessionsQuery(false).queryKey
  const requestedSessionID = (input: StartInput, intentID: string) =>
    review || input.view === "chat" || input.switchFrom ? `session-${intentID}` : undefined
  const open = (machine: string, sessionId: string, replace = false) => void navigate({
    to: "/sessions/$machine/$sessionId", params: { machine, sessionId }, replace,
    search: review ? sessionSearch : undefined,
  })
  const onRoute = (id: string) => router.state.location.pathname.endsWith(`/${id}`)
  function accepted(info: SessionInfo, input: StartInput, intentID: string) {
    const id = requestedSessionID(input, intentID)
    if (!input.forkFrom && !input.switchFrom) saveStartChoices(input.directory, {
      harness: input.harness, mode: input.mode, view: input.view,
      ...(input.worktree !== undefined && { workspace: input.worktree === "new" ? "new" : "folder" }),
    })
    queryClient.setQueryData<SessionInfo[]>(key, (current) => {
      const replicated = current?.find((row) => row.id === info.id && !row.provisionalStart)
      return [replicated ?? {
        ...info,
        state: input.view === "terminal" ? { ...info.state, interactive: true } : info.state,
        node_id: info.node_id ?? (input.machine === LOCAL ? undefined : input.machine),
        started_at: info.started_at ?? new Date().toISOString(),
        provisionalStart: "replica",
      }, ...(current ?? []).filter((row) => row.id !== id && row.id !== info.id)]
    })
    review?.onAccepted?.(input)
    if (review) {
      // Remove the sheet from the history entry that Back will return to.
      if (router.state.location.search.new === "1" || router.state.location.search.new === 1) {
        void navigate({ to: ".", search: (prev) => ({ ...prev, new: undefined, machine: undefined, folder: undefined }), replace: true })
          .then(() => open(input.machine, info.id))
      } else {
        open(input.machine, info.id)
      }
    } else if (!id || (id !== info.id && onRoute(id))) {
      open(input.machine, info.id, Boolean(id))
    }
    void queryClient.invalidateQueries({ queryKey: key })
  }
  const start = useIntent({
    request: (input: StartInput, intentID) => {
      const id = requestedSessionID(input, intentID)
      const path: `/api/${string}` = input.forkFrom ? `/api/sessions/${encodeURIComponent(input.forkFrom)}/fork` : "/api/sessions"
      const claim = api<SessionInfo>(path, {
        machine: input.machine,
        method: "POST",
        body: input.forkFrom ? { id } : {
          ...startSessionBody({ ...input, worktreeName: input.prompt?.trim() }),
          ...(id && (review || input.view === "chat") && { id }),
        },
      }).then((info) => {
        if (!info?.id || !info.state) throw new Error("The gateway did not return a session.")
        return info
      })
      if (!review && id && input.view === "chat") {
        chatClaims.set(id, claim)
        const clear = () => {
          if (chatClaims.get(id) === claim) chatClaims.delete(id)
        }
        void claim.then(clear, clear)
      }
      return claim
    },
    apply: (input, intentID) => {
      const id = requestedSessionID(input, intentID)
      if (review || !id) return
      const info: SessionInfo = {
        id,
        node_id: input.machine === LOCAL ? undefined : input.machine,
        reachable: true,
        directory: input.directory,
        harness: input.harness,
        mode: input.mode,
        transport: input.view === "chat" ? "acp" : "terminal",
        conversation_state: "starting",
        started_at: new Date().toISOString(),
        last_used: new Date().toISOString(),
        state: { interactive: input.view === "terminal", running: false, restarting: false, rows: 0, columns: 0, exit_code: -1 },
        provisionalStart: "claim",
      }
      queryClient.setQueryData<SessionInfo[]>(key, (current) => {
        if (current?.some((row) => row.id === id && row.provisionalStart !== "claim")) return current
        return [info, ...(current ?? []).filter((row) => row.id !== id)]
      })
      open(input.machine, id)
      return () => {
        queryClient.setQueryData<SessionInfo[]>(key, (current) => current?.filter((row) => row.id !== id || row.provisionalStart !== "claim"))
        if (onRoute(id)) {
          const source = input.forkFrom ?? input.switchFrom
          if (source) open(input.machine, source, true)
          else void navigate({ to: "/sessions", replace: true })
        }
      }
    },
    accepted,
    failed: (error, input, retry) => {
      if (review || (input.view !== "chat" && !input.switchFrom)) return
      const action = input.forkFrom ? "fork conversation" : input.switchFrom ? "open terminal" : "start chat"
      hud.error(`Couldn't ${action}: ${error.message}`, input.view === "chat" && reusesOperationId(error) ? retry : undefined)
    },
  })
  const claimID = start.variables && requestedSessionID(start.variables.input, start.variables.id)
  const needsTerminalUpdate = Boolean(review && start.input?.view === "terminal" &&
    start.error instanceof ApiError && start.error.status === 400 && start.error.message === "invalid chat ID")
  return {
    ...start,
    needsTerminalUpdate,
    outcomeUnknown: Boolean(start.error && (reusesOperationId(start.error) || (start.error instanceof ApiError && start.error.status === 408))),
    check: async () => {
      if (!start.variables || !claimID) return false
      const { input, id } = start.variables
      let info: SessionInfo | null
      try {
        info = await queryClient.fetchQuery({ ...sessionRowQuery(input.machine, claimID), staleTime: 0 })
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return false
        throw error
      }
      if (!info || (input.view === "terminal" && !info.state.interactive)) return false
      accepted(info, input, id)
      return true
    },
    mutate: (input: StartInput, options?: Parameters<typeof start.mutate>[2]) => start.mutate(input, undefined, options),
  }
}
