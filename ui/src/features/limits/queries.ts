import { queryOptions } from "@tanstack/react-query"
import { api, LOCAL } from "@/lib/api"
import type { LimitsResponse } from "@/lib/api-types"
import { observedAt } from "./model"

const STORAGE_KEY = "agentpack-limits"
const POLL_MS = 60 * 1000
// A gateway answers with what it last saved and refreshes behind it, so old data is worth rechecking soon.
const RECHECK_MS = 5 * 1000

export function forgetLimits() {
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    // Nothing was stored.
  }
}

export const limitsQuery = (_machine: string, _live: boolean) => {
  return queryOptions({
    queryKey: ["m", LOCAL, "limits"],
    queryFn: async () => {
      const { limits } = await api<LimitsResponse>("/api/limits")
      return limits
    },
    staleTime: POLL_MS,
    // Safety-net polling also covers probes refreshed beside a cached read.
    refetchInterval: (query) => (Date.now() - observedAt(query.state.data ?? [], Date.now()) >= POLL_MS ? RECHECK_MS : POLL_MS),
  })
}
