import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { LimitsResponse } from "@/lib/api-types"

export const limitsQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "limits"],
    queryFn: async () => (await api<LimitsResponse>("/api/limits", { machine })).limits,
    staleTime: 60 * 1000,
    refetchInterval: 60 * 1000,
  })
