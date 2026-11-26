import { queryOptions } from "@tanstack/react-query"
import { api, ApiError } from "@/lib/api"
import { shortAge } from "@/lib/format"
import { array, object, optional, string } from "@/lib/validate"
import type { Connection } from "./connections-model"

// GET /api/agents/shared: how each agent's sign-in is shared on the mesh. Metadata only, never the sign-in.
export type SharedSignIn = {
  harness: string
  state: "shared" | "stopped" | "none" | "expired"
  publisher?: string
  renewer?: string
  expiresAt?: string
  refreshErrorAt?: string
}

// Rows in display order. OMP keeps its sign-in in its own database, so the gateway reports nothing for it.
export const vaultAgents = ["claude", "codex", "cursor", "grok", "opencode", "agy", "omp"]

export function readSharedSignIns(value: unknown): SharedSignIn[] {
  return array(object(value, "shared").agents, "shared.agents").map((raw, index) => {
    const path = `agents[${index}]`
    const row = object(raw, path)
    const state = string(row.state, `${path}.state`)
    return {
      harness: string(row.harness, `${path}.harness`),
      state: state === "shared" || state === "stopped" || state === "expired" ? state : "none",
      publisher: optional(row.publisher_node, `${path}.publisher_node`, string) || undefined,
      renewer: optional(row.renewer_node, `${path}.renewer_node`, string) || undefined,
      expiresAt: optional(row.expires_at, `${path}.expires_at`, string) || undefined,
      refreshErrorAt: optional(row.refresh_error_at, `${path}.refresh_error_at`, string) || undefined,
    }
  })
}

// Under ["agents"], so an agent_credentials state event refetches it.
export const sharedSignInsQuery = queryOptions({
  queryKey: ["agents", "shared"],
  queryFn: async () => readSharedSignIns(await api<unknown>("/api/agents/shared")),
  retry: (count, error) => !(error instanceof ApiError && (error.status === 403 || error.status === 404)) && count < 2,
})

export function signInStatus(harness: string, row: SharedSignIn | undefined, machineName: (node: string) => string, now = Date.now()): string {
  if (harness === "omp") return "Kept in each machine’s own store"
  if (!row || row.state === "none") return "Not shared yet"
  if (row.state === "stopped") return "Sharing stopped"
  if (row.state === "expired") {
    const past = [row.expiresAt, row.refreshErrorAt].find((at) => at && Date.parse(at) <= now)
    return `Expired${past ? ` ${shortAge(new Date(past), now)} ago` : ""} · Sign in again`
  }
  if (!row.renewer) return row.publisher ? `Shared from ${machineName(row.publisher)}` : "Shared"
  const left = row.expiresAt && Date.parse(row.expiresAt) > now ? ` · expires in ${shortAge(new Date(now), Date.parse(row.expiresAt))}` : ""
  return `Shared · renewed by ${machineName(row.renewer)}${left}`
}

// The badge counts only what is broken: expired or failed sign-ins and MCP logins that need one. Never none or stopped.
export function vaultBadge(signIns: SharedSignIn[], connections: Connection[]): number {
  return signIns.filter((row) => row.state === "expired").length + connections.filter((item) => item.status === "needs_login").length
}

// A shared secret is an env credential; this approval hands it to agents too.
export const AGENTS_MODULE = "agentpack:agents"

export const secretReach = (modules: string[]) => (modules.includes(AGENTS_MODULE) ? "MCP servers · agents" : "MCP servers")

// These would let a shared value change what runs, so they are never given to agents. The gateway refuses the same names.
export const refusedForAgents = (name: string) => /^(PATH|HOME|SHELL)$|^(LD_|DYLD_|AGENTPACK_)/.test(name)

// Allowed, with a warning in the sheet.
export const changesAgent = (name: string) => ["ANTHROPIC_API_KEY", "OPENAI_API_KEY", "NODE_OPTIONS"].includes(name)
