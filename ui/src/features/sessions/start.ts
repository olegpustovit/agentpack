import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate, useRouter } from "@tanstack/react-router"
import { api, LOCAL } from "@/lib/api"
import { hud } from "@/lib/hud"
import type { SessionInfo } from "@/lib/api-types"
import { clearFirstMessage, markNewChat, saveStartChoices } from "./choices"
import { sessionsQuery } from "./queries"
import { reusesOperationId, startSessionBody, type SessionView } from "./rules"

// `prompt` is a new chat's first message, which the chat sends once the agent is up.
// `worktree` is "new", an existing worktree of directory, or "" for the folder itself (remembered as the
// choice); a new worktree's branch is named from the prompt.
export type StartInput = { machine: string; directory: string; harness: string; mode: string; view: SessionView; agentSessionId?: string; prompt?: string; worktree?: string; carry?: boolean }

// Starts a session and opens it. Shared by New session, the folder "+" and the Sessions home task bar.
export function useStartSession() {
  const navigate = useNavigate()
  const router = useRouter()
  const queryClient = useQueryClient()
  const key = sessionsQuery(LOCAL, false).queryKey
  const open = (machine: string, sessionId: string) => void navigate({ to: "/sessions/$machine/$sessionId", params: { machine, sessionId } })
  const onRoute = (id: string) => router.state.location.pathname.endsWith(`/${id}`)
  const start = useMutation({
    mutationFn: (input: StartInput & { id?: string }) => api<SessionInfo>("/api/sessions", {
      machine: input.machine,
      method: "POST",
      body: { ...startSessionBody({ ...input, worktreeName: input.prompt?.trim() }), ...(input.id && { id: input.id }) },
    }),
    onMutate: (input) => {
      if (!input.id) return
      const info: SessionInfo = {
        id: input.id,
        node_id: input.machine === LOCAL ? undefined : input.machine,
        reachable: true,
        directory: input.directory,
        harness: input.harness,
        mode: input.mode,
        transport: "acp",
        conversation_state: "starting",
        started_at: new Date().toISOString(),
        last_used: new Date().toISOString(),
        state: { interactive: false, running: false, restarting: false, rows: 0, columns: 0, exit_code: -1 },
        provisionalStart: "claim",
      }
      if (!input.agentSessionId) markNewChat(input.id, input.prompt?.trim())
      queryClient.setQueryData<SessionInfo[]>(key, (current) => [info, ...(current ?? []).filter((row) => row.id !== info.id)])
      open(input.machine, input.id)
    },
    onSuccess: (info, input) => {
      saveStartChoices(input.directory, { harness: input.harness, mode: input.mode, view: input.view, ...(input.worktree !== undefined && { workspace: input.worktree ? "new" : "folder" }) })
      if (input.id) {
        // During a rolling update an older gateway can answer with an ID it generated itself.
        if (input.id !== info.id) {
          clearFirstMessage(input.id)
          if (!input.agentSessionId) markNewChat(info.id, input.prompt?.trim())
          if (onRoute(input.id)) open(input.machine, info.id)
        }
        queryClient.setQueryData<SessionInfo[]>(key, (current) => {
          const replicated = current?.find((row) => row.id === info.id && !row.provisionalStart)
          return [replicated ?? {
            ...info,
            node_id: info.node_id ?? (input.machine === LOCAL ? undefined : input.machine),
            provisionalStart: "replica",
          }, ...(current ?? []).filter((row) => row.id !== input.id && row.id !== info.id)]
        })
      } else {
        open(input.machine, info.id)
      }
      void queryClient.invalidateQueries({ queryKey: key })
    },
    onError: (error, input) => {
      if (!input.id) return
      queryClient.setQueryData<SessionInfo[]>(key, (current) => current?.filter((row) => row.id !== input.id || !row.provisionalStart))
      if (onRoute(input.id)) void navigate({ to: "/sessions" })
      hud.error(`Couldn't start chat: ${error.message}`, reusesOperationId(error) ? () => start.mutate(input) : undefined)
    },
  })
  return {
    ...start,
    mutate: (input: StartInput, options?: Parameters<typeof start.mutate>[1]) => {
      const bytes = input.view === "chat" ? crypto.getRandomValues(new Uint8Array(16)) : undefined
      const id = bytes && `session-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")}`
      start.mutate({ ...input, id }, options)
    },
  }
}
