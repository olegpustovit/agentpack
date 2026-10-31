import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { LimitSnapshot, LimitsResponse } from "@/lib/api-types"
import { observedAt } from "./model"

const STORAGE_KEY = "agentpack-limits"
const POLL_MS = 60 * 1000
// A gateway answers with what it last saved and refreshes behind it, so old data is worth rechecking soon.
const RECHECK_MS = 5 * 1000

type Stored = Record<string, { at: number; limits: LimitSnapshot[] }>

// Private windows may refuse storage, and it can hold anything; the limits then load from the machine.
function stored(): Stored {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}")
    return value && typeof value === "object" ? (value as Stored) : {}
  } catch {
    return {}
  }
}

// Survives a reload, so the view paints at once and an offline machine keeps its last known limits.
export function rememberLimits(machine: string, limits: LimitSnapshot[]) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...stored(), [machine]: { at: Date.now(), limits } }))
  } catch {
    // The in-memory query cache still holds them.
  }
}

export function forgetLimits() {
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    // Nothing was stored.
  }
}

export const limitsQuery = (machine: string, _live: boolean) => {
  const saved = stored()[machine]
  return queryOptions({
    queryKey: ["m", machine, "limits"],
    queryFn: async () => {
      const { limits } = await api<LimitsResponse>("/api/limits", { machine })
      rememberLimits(machine, limits)
      return limits
    },
    initialData: Array.isArray(saved?.limits) ? saved.limits : undefined,
    initialDataUpdatedAt: saved?.at,
    staleTime: POLL_MS,
    // No event announces new limits, so this polls while connected too.
    refetchInterval: (query) => (Date.now() - observedAt(query.state.data ?? [], Date.now()) >= POLL_MS ? RECHECK_MS : POLL_MS),
  })
}
