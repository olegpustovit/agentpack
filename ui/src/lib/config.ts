import { queryOptions } from "@tanstack/react-query"
import { api } from "./api"

export const configQuery = queryOptions({
  queryKey: ["config"],
  queryFn: () => api<{ ui_build_id: string; conversation_events?: boolean }>("/api/config"),
  staleTime: Infinity,
})
