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

// The gateway refreshes tokens before they expire, so an expiry is shown only when sign-in is needed.
export function connectionStatus(connection: Connection): { text: string; problem: boolean } {
  if (connection.status === "needs_login") return { text: "Needs sign-in. Reconnect to restore it for every agent.", problem: true }
  const kind = { oauth: "Signed in", header: "Token header", env: "Environment variables" }[connection.kind] ?? connection.kind
  return { text: `${kind} · shared with every machine`, problem: false }
}

export const connectionsQuery = (live: boolean) => queryOptions({
  queryKey: ["mcp-credentials"],
  queryFn: async () => readConnections(await api<unknown>("/api/mcp/credentials")),
  refetchInterval: live ? false : 30_000,
})

// Sign-in leaves for the authorization server and comes back through the gateway to return_to (Settings → Connections by default).
export async function signIn(input: { url: string; label?: string; client_id?: string; client_secret?: string; return_to?: string }) {
  const started = await api<{ authorize_url: string }>("/api/mcp/logins", { body: input })
  location.assign(started.authorize_url)
}

export type ServerLogin = "connected" | "needs_login" | "none"

// A remote server uses the shared login whose canonical URL equals its own (mcp.LoginURL).
export function serverLogin(url: string, credentials: Connection[]): ServerLogin {
  const connection = url ? credentials.find((item) => item.kind !== "env" && item.match === url) : undefined
  return !connection ? "none" : connection.status === "needs_login" ? "needs_login" : "connected"
}
