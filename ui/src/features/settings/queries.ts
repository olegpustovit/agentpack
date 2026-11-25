import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { Settings } from "@/lib/api-types"

export const settingsQuery = queryOptions({
  queryKey: ["settings"],
  queryFn: () => api<Settings>("/api/settings"),
  staleTime: 5 * 60 * 1000,
})

// Mesh-wide defaults for new chats; a peer without the route reads as Needs update.
export type AgentDefaults = {
  agentpack_tools: boolean
  replace_subagents: boolean
  bypass_permissions?: boolean
  auto_approve_helpers?: boolean
}
export const agentDefaultsQuery = queryOptions({
  queryKey: ["agent-defaults"],
  queryFn: () => api<AgentDefaults>("/api/agents/defaults"),
})
