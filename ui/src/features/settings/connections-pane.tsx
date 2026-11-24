import { useState, type FormEvent, type ReactNode } from "react"
import { Plus } from "lucide-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger, RowMoreButton } from "@/components/kit/overlays"
import { DropdownMenuSeparator } from "@/components/ui/dropdown-menu"
import { LoginGlyph } from "@/features/library/mcp-inspector"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useEventsConnected } from "@/features/events/use-events"
import { api, errorText } from "@/lib/api"
import { hud } from "@/lib/hud"
import { connectionsQuery, loginsFor, serverName, signIn, type Connection } from "./connections-model"
import { LoadError, Pane } from "./panes"

export function ConnectionsPane() {
  const [adding, setAdding] = useState("")
  const connections = useQuery(connectionsQuery(useEventsConnected()))
  if (connections.error) return <LoadError what="MCP connections" error={connections.error} retry={() => void connections.refetch()} />
  if (!connections.data) return <RowSkeleton lines={2} />
  const remote = connections.data.credentials.filter((connection) => connection.match)
  const servers = [...new Set(remote.map((connection) => connection.match))]
  const local = connections.data.credentials.filter((connection) => !connection.match)
  return (
    <Pane title="Connections">
      <div className="flex flex-col gap-(--s6)">
        <Banner>
          Sign in to a remote MCP server once and every machine in this mesh can use it. With more than one login, projects use the default unless you pick another in New session.
        </Banner>
        {connections.data.credentials.length === 0 && (
          <Empty title="No connections">Add a server below, or run <code>agentpack mcp login</code>.</Empty>
        )}
        {servers.map((match) => <ServerGroup key={match} match={match} logins={loginsFor(match, remote)} onAdd={() => setAdding(match)} />)}
        {local.length > 0 && (
          <Section title="Local servers">
            <LoginList>{local.map((connection) => <ConnectionRow key={connection.id} connection={connection} single />)}</LoginList>
          </Section>
        )}
        <AddConnection key={adding} initialURL={adding} remote={remote} callbackURL={connections.data.callbackURL} />
        {connections.data.credentials.some((connection) => connection.kind === "oauth") && <RevokeAll />}
      </div>
    </Pane>
  )
}

function LoginList({ children }: { children: ReactNode }) {
  return <ul className="overflow-hidden rounded-(--r-pop) border-[0.5px] border-(--sep) bg-(--window)">{children}</ul>
}

function ServerGroup({ match, logins, onAdd }: { match: string; logins: Connection[]; onAdd: () => void }) {
  const name = serverName(match)
  const oauth = logins.some((connection) => connection.kind === "oauth")
  return (
    <Section
      title={<>{name}<span className="font-normal text-(--fg3)"> · {new URL(match).host}</span></>}
      action={oauth && <Button size="sm" onClick={onAdd}><Plus aria-hidden className="size-3.5" />Add login…</Button>}
    >
      <LoginList>{logins.map((connection, index) => <ConnectionRow key={connection.id} connection={connection} single={logins.length === 1} first={index === 0} />)}</LoginList>
      {logins.length > 1 && (
        <p className="mt-(--s2) text-(length:--fs-xs) text-(--fg3)">The first login added is the default. Pick another for a project in New session.</p>
      )}
    </Section>
  )
}

function ConnectionRow({ connection, single, first = false }: { connection: Connection; single: boolean; first?: boolean }) {
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
  const problem = connection.status === "needs_login"
  const kind = { oauth: "Signed in", header: "Token header", env: "Variable" }[connection.kind] ?? connection.kind
  const plugins = connection.plugins.map((module) => module.split("/").pop() ?? module)
  return (
    <li className="group/row flex min-w-0 items-center gap-(--s4) border-t-[0.5px] border-(--sep-soft) px-(--s5) py-(--s3) first:border-t-0">
      <LoginGlyph login={problem ? "needs_login" : "connected"} />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-(--s3)">
          <span className="truncate font-medium">{connection.label}</span>
          {first && !single && <Tag>Default</Tag>}
        </div>
        <p className={problem ? "truncate text-(length:--fs-xs) text-(--danger-word)" : "truncate text-(length:--fs-xs) text-(--fg2)"}>
          {problem ? "Needs sign-in" : `${kind} · shared with every machine`}
          {!connection.match && ` · credential = "${connection.label}" · ${connection.names.join(", ")}`}
        </p>
        {plugins.length > 0 && (
          <p className="truncate text-(length:--fs-xs) text-(--fg3)" title={connection.plugins.join(", ")}>
            {`${plugins.join(", ")} may use it`}
          </p>
        )}
      </div>
      {problem && connection.kind === "oauth" && (
        <Button size="sm" variant="primary" disabled={reconnect.isPending} onClick={() => reconnect.mutate()}>Reconnect…</Button>
      )}
      <Menu>
        <MenuTrigger render={<RowMoreButton aria-label={`${connection.label} actions`} className="data-popup-open:opacity-100" />} />
        <MenuContent align="end">
          {connection.kind === "oauth" && <MenuItem onClick={() => reconnect.mutate()}>Reconnect…</MenuItem>}
          {connection.plugins.length > 0 && <DropdownMenuSeparator className="mx-[11px] my-[5px]" />}
          {connection.plugins.map((module) => (
            <MenuItem key={module} onClick={() => revoke.mutate(module)}>{`Revoke ${module.split("/").pop()}`}</MenuItem>
          ))}
          <DropdownMenuSeparator className="mx-[11px] my-[5px]" />
          <MenuItem danger onClick={() => setRemoving(true)}>Remove…</MenuItem>
        </MenuContent>
      </Menu>
      <ConfirmAlert open={removing} onOpenChange={setRemoving}>
        {removing && <ConfirmAlertContent
          title={connection.match ? `Remove the ${serverName(connection.match)} login “${connection.label}”?` : `Remove “${connection.label}”?`}
          description={first && !single
            ? "Agents on every machine lose this login at their next launch, and the next login becomes the default. A signed-in token is also revoked where the server supports it."
            : "Agents on every machine lose this login at their next launch. A signed-in token is also revoked where the server supports it."}
          destructiveLabel="Remove"
          onConfirm={() => { setRemoving(false); remove.mutate() }}
        />}
      </ConfirmAlert>
    </li>
  )
}

function Tag({ children }: { children: ReactNode }) {
  return <span className="shrink-0 rounded-(--r-row) border-[0.5px] border-(--sep) px-(--s2) text-(length:--fs-xs) whitespace-nowrap text-(--fg2)">{children}</span>
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
          title="Sign out of all MCP logins?"
          description="Every signed-in server is revoked on every machine and must be reconnected before agents can use it. Token headers stay."
          destructiveLabel="Sign out everywhere"
          onConfirm={() => { setConfirming(false); revoke.mutate() }}
        />}
      </ConfirmAlert>
    </Section>
  )
}

type Method = "oauth" | "header" | "env"

function AddConnection({ callbackURL, initialURL, remote }: { callbackURL: string; initialURL: string; remote: Connection[] }) {
  const queryClient = useQueryClient()
  const [method, setMethod] = useState<Method>("oauth")
  const [url, setURL] = useState(initialURL)
  const [tried, setTried] = useState(false)
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
  const taken = loginsFor(url.trim().replace(/\/+$/, ""), remote).map((connection) => connection.label)
  // Saving under an existing label replaces that login (it is how Reconnect works), so a second login needs its own name.
  const duplicate = method === "oauth" && taken.length > 0 && (!label.trim() || taken.includes(label.trim()))
  function submit(event: FormEvent) {
    event.preventDefault()
    setTried(true)
    if (duplicate) return
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
            <Input id="mcp-label" required={method === "env"} autoFocus={Boolean(initialURL)}
              placeholder={method === "env" ? "Servers name it with credential = \"label\"" : method === "oauth" && taken.length > 0 ? "Name this login, e.g. Client workspace" : "Defaults to the host"} value={label} onChange={(event) => setLabel(event.target.value)} />
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
        {tried && duplicate && <FieldError>{label.trim() ? `A login named ${label.trim()} exists. Choose another name.` : "Name this login so you can tell it apart from the others."}</FieldError>}
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
