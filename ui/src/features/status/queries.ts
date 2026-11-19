import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { Status } from "@/lib/api-types"

export const statusQuery = queryOptions({
  queryKey: ["status"],
  queryFn: () => api<Status>("/api/status"),
  // Navigation never waits on it: sign-in changes invalidate it and a 401 anywhere redirects to /login.
  staleTime: Infinity,
})
