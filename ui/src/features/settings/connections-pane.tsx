import { useState, type FormEvent } from "react"
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useEventsConnected } from "@/features/events/use-events"
import { errorText } from "@/features/library/mutations"
import { api } from "@/lib/api"
import { hud } from "@/lib/hud"
import { connectionStatus, readConnections, type Connection } from "./connections-model"
import { LoadError, Pane } from "./panes"

export const connectionsQuery = (live: boolean) => queryOptions({
  queryKey: ["mcp-credentials"],
  queryFn: async () => readConnections(await api<unknown>("/api/mcp/credentials")),
  refetchInterval: live ? false : 30_000,
})

// Sign-in leaves for the authorization server and returns through the gateway to this pane.
async function signIn(input: { url: string; label?: string; client_id?: string; client_secret?: string }) {
  const started = await api<{ authorize_url: string }>("/api/mcp/logins", { body: input })
  location.assign(started.authorize_url)
}

export function ConnectionsPane() {
  const connections = useQuery(connectionsQuery(useEventsConnected()))
  if (connections.error) return <LoadError what="MCP connections" error={connections.error} retry={() => void connections.refetch()} />
  if (!connections.data) return <RowSkeleton lines={2} />
  return (
    <Pane title="Connections">
      <div className="flex flex-col gap-(--s6)">
        <Banner>
          Connect a remote MCP server once. Every agent on every machine in this mesh uses it, starting with its next launch.
        </Banner>
        <Section title="Connections">
          {connections.data.credentials.length === 0 ? (
            <Empty title="No connections">Add a server below, or run <code>agentpack mcp login</code>.</Empty>
          ) : (
            connections.data.credentials.map((connection) => <ConnectionRow key={connection.id} connection={connection} />)
          )}
        </Section>
        <AddConnection callbackURL={connections.data.callbackURL} />
        {connections.data.credentials.some((connection) => connection.kind === "oauth") && <RevokeAll />}
      </div>
    </Pane>
  )
}

function ConnectionRow({ connection }: { connection: Connection }) {
  const queryClient = useQueryClient()
  const [removing, setRemoving] = useState(false)
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["mcp-credentials"] })
  const remove = useMutation<unknown, Error, void>({
    mutationFn: () => api(`/api/mcp/credentials/${encodeURIComponent(connection.id)}`, { method: "DELETE" }),
    onSuccess: () => {
      hud.success(`Removed ${connection.label} on every machine.`)
      void refresh()
    },
    onError: (error) => hud.error(`Couldn't remove ${connection.label}: ${errorText(error)}`, () => remove.mutate()),
  })
  const revoke = useMutation<unknown, Error, string>({
    mutationFn: (module) => api(`/api/mcp/credentials/${encodeURIComponent(connection.id)}/approvals`, { method: "DELETE", body: { module } }),
    onSuccess: () => void refresh(),
    onError: (error, module) => hud.error(`Couldn't revoke ${module}: ${errorText(error)}`, () => revoke.mutate(module)),
  })
  const reconnect = useMutation<void, Error, void>({
    mutationFn: () => signIn({ url: connection.match, label: connection.label }),
    onError: (error) => hud.error(`Couldn't reconnect ${connection.label}: ${errorText(error)}`, () => reconnect.mutate()),
  })
  const status = connectionStatus(connection)
  return (
    <div className="flex min-w-0 items-center gap-(--s4) border-b-[0.5px] border-(--sep) py-(--s3)">
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium">{connection.label}</p>
        <p className="truncate text-(length:--fs-sm) text-(--fg2)" title={connection.match}>
          {connection.match || `Local servers with credential = "${connection.label}" · ${connection.names.join(", ")}`}
        </p>
        <p className={status.problem ? "text-(length:--fs-sm) text-(--danger)" : "text-(length:--fs-xs) text-(--fg3)"}>{status.text}</p>
        {connection.plugins.map((module) => (
          <p key={module} className="flex items-center gap-(--s2) text-(length:--fs-xs) text-(--fg3)">
            <span className="truncate">{`Plugin ${module} may use it`}</span>
            <Button variant="ghost" size="sm" disabled={revoke.isPending} onClick={() => revoke.mutate(module)}>Revoke</Button>
          </p>
        ))}
      </div>
      {connection.kind === "oauth" && (
        <Button variant={status.problem ? "default" : "ghost"} disabled={reconnect.isPending} onClick={() => reconnect.mutate()}>
          Reconnect
        </Button>
      )}
      <Button variant="ghost" disabled={remove.isPending} onClick={() => setRemoving(true)}>Remove</Button>
      <ConfirmAlert open={removing} onOpenChange={setRemoving}>
        {removing && <ConfirmAlertContent
          title={`Remove ${connection.label}?`}
          description="Agents on every machine lose this login at their next launch. A signed-in token is also revoked where the server supports it."
          destructiveLabel="Remove"
          onConfirm={() => { setRemoving(false); remove.mutate() }}
        />}
      </ConfirmAlert>
    </div>
  )
}

// After a machine leaves the mesh it keeps the tokens it received; revoking them makes every login sign in again.
function RevokeAll() {
  const queryClient = useQueryClient()
  const [confirming, setConfirming] = useState(false)
  const revoke = useMutation<{ revoked: number }, Error, void>({
    mutationFn: () => api<{ revoked: number }>("/api/mcp/credentials/revoke-all", { method: "POST" }),
    onSuccess: ({ revoked }) => {
      hud.success(`Signed out of ${revoked} login${revoked === 1 ? "" : "s"}. Reconnect each one to use it again.`)
      void queryClient.invalidateQueries({ queryKey: ["mcp-credentials"] })
    },
    onError: (error) => hud.error(`Couldn't sign out of the logins: ${errorText(error)}`, () => revoke.mutate()),
  })
  return (
    <Section title="Removed a machine?">
      <div className="flex items-center justify-between gap-(--s6)">
        <p>A machine that left the mesh keeps the logins it received. Sign out of every login, then reconnect the ones you use. Token headers stay; replace them where they were issued.</p>
        <Button variant="destructive" disabled={revoke.isPending} onClick={() => setConfirming(true)}>Sign out of all…</Button>
      </div>
      <ConfirmAlert open={confirming} onOpenChange={setConfirming}>
        {confirming && <ConfirmAlertContent
          title="Sign out of every MCP login?"
          description="Each signed-in server is revoked on every machine and needs Reconnect before agents can use it."
          destructiveLabel="Sign out of all"
          onConfirm={() => { setConfirming(false); revoke.mutate() }}
        />}
      </ConfirmAlert>
    </Section>
  )
}

type Method = "oauth" | "header" | "env"

function AddConnection({ callbackURL }: { callbackURL: string }) {
  const queryClient = useQueryClient()
  const [method, setMethod] = useState<Method>("oauth")
  const [url, setURL] = useState("")
  const [label, setLabel] = useState("")
  const [clientID, setClientID] = useState("")
  const [clientSecret, setClientSecret] = useState("")
  const [header, setHeader] = useState("Authorization")
  const [value, setValue] = useState("")
  const [variable, setVariable] = useState("")
  const add = useMutation<void, Error, void>({
    mutationFn: async () => {
      if (method === "oauth") return signIn({ url, label, client_id: clientID, client_secret: clientSecret })
      if (method === "env") await api("/api/mcp/credentials", { body: { kind: "env", label, values: { [variable]: value } } })
      else await api("/api/mcp/credentials", { body: { kind: "header", url, label, values: { [header]: value } } })
    },
    onSuccess: () => {
      if (method === "oauth") return
      hud.success(method === "env" ? `Saved ${variable} as ${label}. Local servers use it with credential = "${label}".` : `Saved the ${header} header for every machine.`)
      setURL("")
      setLabel("")
      setValue("")
      void queryClient.invalidateQueries({ queryKey: ["mcp-credentials"] })
    },
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    add.mutate()
  }
  return (
    <Section title="Add connection">
      <form className="flex flex-col gap-(--s4)" onSubmit={submit}>
        <SegmentedControl aria-label="Connection method" value={[method]} onValueChange={(values) => {
          if (values[0] === "oauth" || values[0] === "header" || values[0] === "env") setMethod(values[0])
        }}>
          <SegmentedControlItem value="oauth">Sign in</SegmentedControlItem>
          <SegmentedControlItem value="header">Token header</SegmentedControlItem>
          <SegmentedControlItem value="env">Variable</SegmentedControlItem>
        </SegmentedControl>
        <FieldGrid>
          {method !== "env" && (
            <Field>
              <FieldLabel htmlFor="mcp-url">Server URL</FieldLabel>
              <Input id="mcp-url" type="url" required placeholder="https://api.githubcopilot.com/mcp/" value={url} onChange={(event) => setURL(event.target.value)} />
            </Field>
          )}
          <Field>
            <FieldLabel htmlFor="mcp-label">Label</FieldLabel>
            <Input id="mcp-label" required={method === "env"} placeholder={method === "env" ? "Servers name it with credential = \"label\"" : "Defaults to the host"} value={label} onChange={(event) => setLabel(event.target.value)} />
          </Field>
          {method === "env" ? (
            <>
              <Field>
                <FieldLabel htmlFor="mcp-variable">Variable</FieldLabel>
                <Input id="mcp-variable" required pattern="[A-Za-z_][A-Za-z0-9_]*" placeholder="GITHUB_PERSONAL_ACCESS_TOKEN" value={variable} onChange={(event) => setVariable(event.target.value)} />
              </Field>
              <Field>
                <FieldLabel htmlFor="mcp-variable-value">Value</FieldLabel>
                <Input id="mcp-variable-value" type="password" autoComplete="off" required value={value} onChange={(event) => setValue(event.target.value)} />
              </Field>
            </>
          ) : method === "oauth" ? (
            <>
              <Field>
                <FieldLabel htmlFor="mcp-client">Client ID</FieldLabel>
                <Input id="mcp-client" placeholder="Optional; registered automatically when the server allows it" value={clientID} onChange={(event) => setClientID(event.target.value)} />
              </Field>
              {clientID && (
                <Field>
                  <FieldLabel htmlFor="mcp-client-secret">Client secret</FieldLabel>
                  <Input id="mcp-client-secret" type="password" autoComplete="off" placeholder="Only for confidential clients" value={clientSecret} onChange={(event) => setClientSecret(event.target.value)} />
                </Field>
              )}
            </>
          ) : (
            <>
              <Field>
                <FieldLabel htmlFor="mcp-header">Header</FieldLabel>
                <Input id="mcp-header" required value={header} onChange={(event) => setHeader(event.target.value)} />
              </Field>
              <Field>
                <FieldLabel htmlFor="mcp-value">Value</FieldLabel>
                <Input id="mcp-value" type="password" autoComplete="off" required placeholder="Bearer …" value={value} onChange={(event) => setValue(event.target.value)} />
              </Field>
            </>
          )}
        </FieldGrid>
        {add.error && <FieldError>{errorText(add.error)}</FieldError>}
        {method === "oauth" && clientID && (
          <p className="text-(length:--fs-sm) text-(--fg2)">Register this redirect URL with the OAuth app: <code>{callbackURL}</code></p>
        )}
        <div>
          <Button type="submit" disabled={add.isPending}>{method === "oauth" ? "Sign in…" : "Save"}</Button>
        </div>
      </form>
    </Section>
  )
}
