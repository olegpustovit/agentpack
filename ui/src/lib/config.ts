import { queryOptions } from "@tanstack/react-query"
import { api } from "./api"

export const configQuery = queryOptions({
  queryKey: ["config"],
  queryFn: () => api<{ ui_build_id: string; conversation_events?: boolean; conversation_config_log?: boolean; debug_chat_timings?: boolean }>("/api/config"),
  staleTime: Infinity,
})
