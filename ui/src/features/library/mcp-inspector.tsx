import { Link } from "@tanstack/react-router"
import { useMutation, useQuery } from "@tanstack/react-query"
import { Plug } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useEventsConnected } from "@/features/events/use-events"
import { connectionsQuery, serverLogin, signIn, type ServerLogin } from "@/features/settings/connections-model"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { errorText } from "./mutations"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { plural } from "@/lib/format"
import { mcpUses } from "./inspect"
import { Failures, MissingItem } from "./inspector-state"
import type { MCPItem } from "./model"
import { useLibrary } from "./use-library"

// Lock statuses from pack.lock. Unknown values show as recorded.
const statusWords: Record<string, string> = { pinned: "Pinned", unpinned: "Unpinned", unpinnable: "Unpinnable" }

// Launch commands, environment and headers never leave the machine, so the inspector has none to show.
export function McpInspector({ name }: { name: string }) {
  const { inputs, items, loading, failures } = useLibrary()
  const server = items.find((entry): entry is MCPItem => entry.type === "mcp" && entry.name === name)
  if (!server) {
    return (
      <MissingItem loading={loading} failures={failures} title="MCP server not found">
        {`No project on any machine uses ${name}.`}
      </MissingItem>
    )
  }

  const uses = mcpUses(inputs, server)
  const statuses = [...new Set(uses.map((use) => statusWords[use.status] ?? use.status))]

  return (
    <Inspector>
      <Failures failures={failures} />
      <InspectorHead
        icon={<Plug aria-hidden />}
        title={server.name}
        subtitle={`${server.launcher} · ${plural(server.projects.length, "project")} · ${server.machines.join(", ")}`}
      />
      <Section title="Server" className="col-span-full">
        <dl className="flex flex-col gap-(--s3)">
          <KeyValue label="Launcher">{server.launcher || "—"}</KeyValue>
          <KeyValue label="Status">{statuses.join(", ") || "—"}</KeyValue>
          <KeyValue label="Version">{server.version || "Not pinned"}</KeyValue>
          {server.launcher === "remote" && (
            <KeyValue label="Login">
              <LoginControl server={server} />
            </KeyValue>
          )}
        </dl>
      </Section>
      <Section title="Used in" className="col-span-full">
        <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">{plural(uses.length, "project")}</p>
        <DataGrid>
          <DataGridHead>
            <tr>
              <DataGridHeaderCell className="w-40">Machine</DataGridHeaderCell>
              <DataGridHeaderCell>Project</DataGridHeaderCell>
              <DataGridHeaderCell className="w-28">Version</DataGridHeaderCell>
              <DataGridHeaderCell className="w-28">Status</DataGridHeaderCell>
              <DataGridHeaderCell className="w-56">Integrity</DataGridHeaderCell>
            </tr>
          </DataGridHead>
          <tbody>
            {uses.map((use) => (
              <DataGridRow key={`${use.machine}\0${use.directory}`}>
                <DataGridCell>{use.machine}</DataGridCell>
                <DataGridCell title={use.directory}>{use.project}</DataGridCell>
                <DataGridCell>{use.version || "Not pinned"}</DataGridCell>
                <DataGridCell>{statusWords[use.status] ?? use.status}</DataGridCell>
                <DataGridCell title={use.integrity}>
                  <code>{use.integrity || "—"}</code>
                </DataGridCell>
              </DataGridRow>
            ))}
          </tbody>
        </DataGrid>
      </Section>
    </Inspector>
  )
}

export const loginWords: Record<ServerLogin, string> = { connected: "Connected", needs_login: "Needs sign-in", none: "Not connected" }

// Filled when connected, warning when the login expired, hollow when there is none.
export function LoginGlyph({ login, className }: { login: ServerLogin; className?: string }) {
  const look = { connected: "bg-(--ok) border-(--ok)", needs_login: "bg-(--warn) border-(--warn)", none: "border-(--fg3)" }[login]
  return <span aria-hidden className={cn("inline-block size-(--s3) shrink-0 rounded-full border sel:border-(--accent-fg)", look, login !== "none" && "sel:bg-(--accent-fg)", className)} />
}

// The shared login of a remote server, or undefined while unknown: an older peer sends no URL,
// and a server with its own credential, Authorization header or URL query cannot use one.
export function useServerLogin(server: MCPItem): ServerLogin | undefined {
  const connections = useQuery(connectionsQuery(useEventsConnected()))
  if (server.launcher !== "remote" || !server.url || !connections.data) return undefined
  return serverLogin(server.url, connections.data.credentials)
}

function LoginControl({ server }: { server: MCPItem }) {
  const login = useServerLogin(server)
  const connect = useMutation<void, Error, void>({
    mutationFn: () => signIn({ url: server.url, label: server.name, return_to: location.pathname + location.search }),
    onError: (error) => hud.error(`Couldn't start the sign-in for ${server.name}: ${errorText(error)}`, () => connect.mutate()),
  })
  const manage = (
    <Link to="/settings/$pane" params={{ pane: "connections" }} className="text-(--accent) underline-offset-2 hover:underline">
      Manage connections…
    </Link>
  )
  if (!login) return manage
  return (
    <span className="flex flex-wrap items-center gap-(--s3)">
      <span className="flex items-center gap-(--s2)">
        <LoginGlyph login={login} />
        {login === "connected" ? "Connected for every agent" : loginWords[login]}
      </span>
      {login === "connected" ? manage : (
        <Button size="sm" variant="primary" disabled={connect.isPending} onClick={() => connect.mutate()}>
          {login === "needs_login" ? "Reconnect…" : "Connect…"}
        </Button>
      )}
    </span>
  )
}
