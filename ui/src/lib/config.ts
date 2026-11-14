import { queryOptions } from "@tanstack/react-query"
import { api } from "./api"

export const configQuery = queryOptions({
  queryKey: ["config"],
  queryFn: () => api<{ ui_build_id: string }>("/api/config"),
  staleTime: Infinity,
})
