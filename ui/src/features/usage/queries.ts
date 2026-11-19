import { queryOptions } from "@tanstack/react-query"
import { api, LOCAL } from "@/lib/api"
import { array, object } from "@/lib/validate"
import type { LimitSample, UsageHour, UsageReport, UsageSession } from "./model"

// Every read is local and replicated, so the page works offline and never fans out to peers.
// State events name usage_hourly, sessions and limit_samples, which refetch it; there is no poll.
export const usageQuery = (from: number, to: number) =>
  queryOptions({
    queryKey: ["m", LOCAL, "usage", from, to],
    queryFn: async (): Promise<UsageReport> => {
      const body = object(await api<unknown>("/api/usage", { query: { from: String(Math.floor(from / 1000)), to: String(Math.ceil(to / 1000)) } }), "usage")
      return {
        hourly: array(body.hourly, "hourly") as UsageHour[],
        sessions: array(body.sessions, "sessions") as UsageSession[],
        samples: array(body.samples, "samples") as LimitSample[],
      }
    },
    staleTime: 60 * 1000,
  })
