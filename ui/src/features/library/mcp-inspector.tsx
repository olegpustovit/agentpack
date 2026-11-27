import { errorText } from "@/lib/api"
import { Link } from "@tanstack/react-router"
import { useMutation, useQuery } from "@tanstack/react-query"
import { Plug } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useEventsConnected } from "@/features/events/use-events"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { connectionsQuery, loginsFor, serverLogin, signIn, type ServerLogin } from "@/features/settings/connections-model"
import type { MCPServerInput } from "@/lib/api-types"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { plural } from "@/lib/format"
import { InstallProjectMenu } from "./install-project-menu"
import { mcpUses, type MCPUse } from "./inspect"
import { Failures, MissingItem } from "./inspector-state"
import type { MCPItem } from "./model"
import { useLibrary } from "./use-library"

// Lock statuses from pack.lock. Unknown values show as recorded.
const statusWords: Record<string, string> = { pinned: "Pinned", unpinned: "Unpinned", unpinnable: "Unpinnable" }

function installableServer(server: MCPItem, uses: MCPUse[]): MCPServerInput | undefined {
  if (server.launcher === "remote" && server.url) {
    return { name: server.name, url: server.url, type: uses.find((use) => use.transport)?.transport || "http" }
  }
  if (server.launcher === "npm" && server.package) {
    return { name: server.name, command: "npx", args: ["-y", server.version ? `${server.package}@${server.version}` : server.package] }
  }
  return undefined
}

// Launch commands, environment and headers never leave the machine, so the inspector has none to show.
export function McpInspector({ name }: { name: string }) {
  const isPhone = useIsPhone()
  const { inputs, items, loading, failures } = useLibrary()
  const server = items.find((entry): entry is MCPItem => entry.type === "mcp" && entry.name === name)
  const login = useServerLogin(server)

  if (!server) {
    return (
      <MissingItem loading={loading} failures={failures} title="MCP server not found">
        {`No project on any machine uses ${name}.`}
      </MissingItem>
    )
  }

  const uses = mcpUses(inputs, server)
  const statuses = [...new Set(uses.map((use) => statusWords[use.status] ?? use.status))]
  const installTarget = installableServer(server, uses)

  function pinStatus(use: MCPUse): string {
    return `${statusWords[use.status] ?? use.status} · ${use.version || "Not pinned"}`
  }

  function accountStatus(use: MCPUse): string {
    if (server?.launcher !== "remote") return "No shared login"
    if (use.projectLogin) return "Project login"
    if (!login) return "Not connected"
    return use.login ? `${use.login} · ${loginWords[login]}` : loginWords[login]
  }

  return (
    <Inspector>
      <Failures failures={failures} />
      <InspectorHead
        icon={<Plug aria-hidden />}
        title={server.name}
        subtitle={`${server.launcher} · ${plural(server.projects.length, "project")} · ${server.machines.join(", ")}`}
      >
        {installTarget && (
          <InstallProjectMenu size="sm" variant="primary" name={server.name} target={{ server: installTarget }}>
            Install in another project…
          </InstallProjectMenu>
        )}
      </InspectorHead>
      <Section title="Server" className="col-span-full">
        <dl className="flex flex-col gap-(--s3)">
          <KeyValue label="Launcher">{server.launcher || "—"}</KeyValue>
          <KeyValue label="Version pin">{[statuses.join(", ") || "—", server.version || "Not pinned"].join(" · ")}</KeyValue>
          <KeyValue label="Account connection">
            {server.launcher === "remote" ? <LoginControl server={server} /> : "Local process · no shared login"}
          </KeyValue>
        </dl>
      </Section>
      <Section title="Used in" className="col-span-full">
        <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">{plural(uses.length, "project")}</p>
        {isPhone ? (
          <ul aria-label="Used in" className="flex min-w-0 flex-col">
            {uses.map((use) => (
              <li key={`${use.machine}\0${use.directory}`} className="flex min-w-0 flex-col gap-1 border-b-[0.5px] border-(--sep) py-(--s2) last:border-b-0">
                <span className="break-words font-medium">{use.project}</span>
                <span className="break-all text-(length:--fs-xs) text-(--fg2)">{`${use.machine} · ${use.directory}`}</span>
                <span className="text-(length:--fs-sm) text-(--fg2)">{`Version pin: ${pinStatus(use)}`}</span>
                <span className="text-(length:--fs-sm) text-(--fg2)">{`Account connection: ${accountStatus(use)}`}</span>
                {use.integrity && (
                  <code className="break-all text-(length:--fs-xs) text-(--fg3)">{use.integrity}</code>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <DataGrid aria-label="Used in">
            <DataGridHead>
              <tr>
                <DataGridHeaderCell className="w-36">Machine</DataGridHeaderCell>
                <DataGridHeaderCell>Project</DataGridHeaderCell>
                <DataGridHeaderCell className="w-40">Version pin</DataGridHeaderCell>
                <DataGridHeaderCell className="w-40">Account connection</DataGridHeaderCell>
                <DataGridHeaderCell className="w-48">Integrity</DataGridHeaderCell>
              </tr>
            </DataGridHead>
            <tbody>
              {uses.map((use) => (
                <DataGridRow key={`${use.machine}\0${use.directory}`}>
                  <DataGridCell>{use.machine}</DataGridCell>
                  <DataGridCell title={use.directory}>{use.project}</DataGridCell>
                  <DataGridCell>{pinStatus(use)}</DataGridCell>
                  <DataGridCell>{accountStatus(use)}</DataGridCell>
                  <DataGridCell title={use.integrity}>
                    <code>{use.integrity || "—"}</code>
                  </DataGridCell>
                </DataGridRow>
              ))}
            </tbody>
          </DataGrid>
        )}
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
export function useServerLogin(server: MCPItem | undefined): ServerLogin | undefined {
  const connections = useQuery(connectionsQuery(useEventsConnected()))
  if (!server || server.launcher !== "remote" || !server.url || !connections.data) return undefined
  return serverLogin(server.url, connections.data.credentials)
}

function LoginControl({ server }: { server: MCPItem }) {
  const login = useServerLogin(server)
  const connections = useQuery(connectionsQuery(useEventsConnected()))
  const logins = loginsFor(server.url ?? "", connections.data?.credentials ?? [])
  const connect = useMutation<void, Error, void>({
    mutationFn: () => signIn({ url: server.url, label: logins[0]?.label ?? server.name, return_to: location.pathname + location.search }),
    onError: (error) => hud.error(`Couldn't start the sign-in for ${server.name}: ${errorText(error)}`, () => connect.mutate()),
  })
  const manage = (
    <Link to="/settings/$pane" params={{ pane: "connections" }} className="text-(--accent-text) underline-offset-2 hover:underline">
      {logins.length > 1 ? "Manage logins…" : "Manage connections…"}
    </Link>
  )
  if (!login) return manage
  const first = logins[0]
  return (
    <span className="flex flex-wrap items-center gap-(--s3)">
      <span className="flex items-center gap-(--s2)">
        <LoginGlyph login={login} />
        {first ? `${first.label}${login === "needs_login" ? " · Needs sign-in" : ""}${logins.length > 1 ? ` · default of ${logins.length} logins` : ""}` : loginWords[login]}
      </span>
      {login === "connected" ? manage : (
        <>
          <Button size="sm" variant="primary" disabled={connect.isPending} onClick={() => connect.mutate()}>
            {login === "needs_login" ? "Reconnect…" : "Sign in…"}
          </Button>
          {manage}
        </>
      )}
    </span>
  )
}
