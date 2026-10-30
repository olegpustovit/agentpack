import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { Settings } from "@/lib/api-types"

export const settingsQuery = queryOptions({
  queryKey: ["settings"],
  queryFn: () => api<Settings>("/api/settings"),
  staleTime: 5 * 60 * 1000,
})
