import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { markNewChat, saveStartChoices } from "./choices"
import { sessionsQuery } from "./queries"
import { startSessionBody, type SessionView } from "./rules"

export type StartInput = { machine: string; directory: string; harness: string; mode: string; view: SessionView; agentSessionId?: string }

// Starts a session and opens it. Shared by New session, the folder "+" and the Sessions home task bar.
export function useStartSession() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: StartInput) => api<SessionInfo>("/api/sessions", { machine: input.machine, method: "POST", body: startSessionBody(input) }),
    onSuccess: (info, input) => {
      saveStartChoices(input.directory, { harness: input.harness, mode: input.mode, view: input.view })
      if (input.view === "chat" && !input.agentSessionId) markNewChat(info.id)
      void queryClient.invalidateQueries({ queryKey: sessionsQuery(input.machine, false).queryKey })
      void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: input.machine, sessionId: info.id } })
    },
  })
}
