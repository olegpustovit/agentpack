import { queryOptions } from "@tanstack/react-query"
import { api, ApiError } from "@/lib/api"
import { array, boolean, object, optional, string } from "@/lib/validate"

export type AgentMethod = { id: string; label: string }

// What an agent reports about its sign-in on one machine. Label is an email, plan or providers.
export type AgentAccount = {
  harness: string
  installed: boolean
  signedIn: boolean
  label?: string
  methods: AgentMethod[]
  /** The gateway can sign it out. */
  signOut: boolean
  error?: string
}

// One sign-in in progress on a machine: the link (and code) to show, and what to paste back.
export type LoginStep = {
  id: string
  url?: string
  code?: string
  input?: "code" | "url"
  state: "waiting" | "done" | "failed"
  error?: string
}

export function readAgents(value: unknown): AgentAccount[] {
  return array(object(value, "agents").agents, "agents.agents").map((raw, index) => {
    const path = `agents[${index}]`
    const item = object(raw, path)
    return {
      harness: string(item.harness, `${path}.harness`),
      installed: boolean(item.installed, `${path}.installed`),
      signedIn: boolean(item.signed_in, `${path}.signed_in`),
      label: optional(item.label, `${path}.label`, string) || undefined,
      methods: (optional(item.methods, `${path}.methods`, array) ?? []).map((method, at) => {
        const fields = object(method, `${path}.methods[${at}]`)
        return { id: string(fields.id, `${path}.methods[${at}].id`), label: string(fields.label, `${path}.methods[${at}].label`) }
      }),
      signOut: optional(item.sign_out, `${path}.sign_out`, boolean) ?? false,
      error: optional(item.error, `${path}.error`, string) || undefined,
    }
  })
}

export function readStep(value: unknown): LoginStep {
  const step = object(value, "login")
  const state = string(step.state, "login.state")
  const input = optional(step.input, "login.input", string)
  return {
    id: string(step.id, "login.id"),
    url: optional(step.url, "login.url", string) || undefined,
    code: optional(step.code, "login.code", string) || undefined,
    input: input === "code" || input === "url" ? input : undefined,
    state: state === "done" || state === "failed" ? state : "waiting",
    error: optional(step.error, "login.error", string) || undefined,
  }
}

// Each machine probes its agent CLIs, so an answer stays fresh for a minute.
export const agentsQuery = (machine: string) =>
  queryOptions({
    queryKey: ["agents", machine],
    queryFn: async () => readAgents(await api<unknown>("/api/agents", { machine })),
    staleTime: 60 * 1000,
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
  })

export const agentPath = (harness: string) => `/api/agents/${encodeURIComponent(harness)}/login` as const
