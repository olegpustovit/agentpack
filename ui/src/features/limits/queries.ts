import { queryOptions } from "@tanstack/react-query"
import { api, LOCAL } from "@/lib/api"
import type { LimitsResponse } from "@/lib/api-types"

const POLL_MS = 60 * 1000

export const limitsQuery = (_machine: string, live: boolean) => {
  return queryOptions({
    queryKey: ["m", LOCAL, "limits"],
    queryFn: async () => {
      const { limits } = await api<LimitsResponse>("/api/limits")
      return limits
    },
    staleTime: POLL_MS,
    // A gateway answers from its saved copy and announces the refresh behind it, so polling covers only a dropped stream.
    refetchInterval: live ? false : POLL_MS,
  })
}
