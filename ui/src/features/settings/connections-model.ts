import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import { array, object, optional, string, type Fields } from "@/lib/validate"

export type Connection = {
  id: string
  match: string
  kind: string
  label: string
  status: string
  names: string[]
  expiresAt: string
  plugins: string[]
}

export type Connections = { credentials: Connection[]; callbackURL: string }

const strings = (value: unknown, path: string) => array(value, path).map((item, index) => string(item, `${path}[${index}]`))

export function readConnections(value: unknown): Connections {
  const body = object(value, "connections")
  return {
    callbackURL: optional(body.callback_url, "callback_url", string) ?? "",
    credentials: array(body.credentials, "credentials").map((item, index) => {
      const row: Fields = object(item, `credentials[${index}]`)
      return {
        id: string(row.id, "id"),
        match: optional(row.match, "match", string) ?? "",
        kind: string(row.kind, "kind"),
        label: string(row.label, "label"),
        status: string(row.status, "status"),
        names: optional(row.names, "names", strings) ?? [],
        expiresAt: optional(row.expires_at, "expires_at", string) ?? "",
        plugins: optional(row.plugins, "plugins", strings) ?? [],
      }
    }),
  }
}

export const connectionsQuery = (live: boolean) => queryOptions({
  queryKey: ["mcp-credentials"],
  queryFn: async () => readConnections(await api<unknown>("/api/mcp/credentials")),
  refetchInterval: live ? false : 30_000,
})

// Sign-in leaves for the authorization server and comes back through the gateway to return_to (Settings → Connections by default).
type SignInInput = { url: string; label?: string; client_id?: string; client_secret?: string; return_to?: string }
type SignInOutcome = { status: "pending" | "connected" | "failed"; credential_id?: string; error?: string }

export async function startSignIn(input: SignInInput) {
  return api<{ login: string; authorize_url: string }>("/api/mcp/logins", { body: input })
}

export async function signIn(input: SignInInput) {
  location.assign((await startSignIn(input)).authorize_url)
}

export function signInOutcome(login: string, signal: AbortSignal) {
  return api<SignInOutcome>(`/api/mcp/logins/${encodeURIComponent(login)}`, { signal })
}

export async function waitForSignIn(login: string, signal: AbortSignal) {
  for (;;) {
    signal.throwIfAborted()
    const outcome = await signInOutcome(login, signal)
    if (outcome.status === "connected" && outcome.credential_id) return outcome.credential_id
    if (outcome.status !== "pending") throw new Error(outcome.error || "Sign-in did not complete.")
    await new Promise((resolve) => setTimeout(resolve, 1_000))
  }
}

export type ServerLogin = "connected" | "needs_login" | "none"

// A remote server uses the shared login whose canonical URL equals its own (mcp.LoginURL).
export function serverLogin(url: string, credentials: Connection[]): ServerLogin {
  const connection = url ? credentials.find((item) => item.kind !== "env" && item.match === url) : undefined
  return !connection ? "none" : connection.status === "needs_login" ? "needs_login" : "connected"
}

// Every saved login for one server in the order the resolver tries them: the first is the default.
export function loginsFor(url: string, credentials: Connection[]): Connection[] {
  return url ? credentials.filter((item) => item.kind !== "env" && item.match === url) : []
}

// Server groups in Settings: one per endpoint, named after its host.
export function serverName(match: string): string {
  try {
    const host = new URL(match).hostname.replace(/^(mcp|api)\./, "")
    const name = host.split(".").at(-2) ?? host
    return name === "githubcopilot" ? "GitHub" : name.charAt(0).toUpperCase() + name.slice(1)
  } catch {
    return match
  }
}
