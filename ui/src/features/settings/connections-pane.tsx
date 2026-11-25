import { useEffect, useState, type FormEvent, type ReactNode } from "react"
import { Plus } from "lucide-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel, FormGroup, FormRow } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger, RowMoreButton, Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { DropdownMenuSeparator } from "@/components/ui/dropdown-menu"
import { LoginGlyph } from "@/features/library/mcp-inspector"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useEventsConnected } from "@/features/events/use-events"
import { api, ApiError, errorText } from "@/lib/api"
import { hud } from "@/lib/hud"
import { connectionsQuery, loginsFor, serverName, signInOutcome, startSignIn, type Connection } from "./connections-model"
import { LoadError, Pane } from "./panes"

const route = getRouteApi("/_app/settings/$pane")
const returningConnectionKey = "agentpack.mcp.returning-connection"

function returningConnection() {
  try {
    return sessionStorage.getItem(returningConnectionKey) || undefined
  } catch {
    return undefined
  }
}

async function openConnectionsLogin(input: Parameters<typeof startSignIn>[0], connection?: string) {
  const search = connection ? `?${new URLSearchParams({ connection })}` : ""
  const login = await startSignIn({ ...input, return_to: `/settings/connections${search}` })
  try {
    sessionStorage.setItem(returningConnectionKey, connection ?? "")
  } catch {
    // The return path still identifies the affected row while the login is valid.
  }
  location.assign(login.authorize_url)
}

type LoginFeedback = { message: string; retry?: boolean; pending?: boolean }
const callbackMessages = {
  denied: "The server declined sign-in.",
  expired: "This sign-in link expired or was already used.",
  canceled: "Sign-in canceled.",
}

export function ConnectionsPane() {
  const [adding, setAdding] = useState<string>()
  const [returning] = useState(returningConnection)
  const { callback, oauth_login: login, connection } = route.useSearch()
  const reported = callback === "denied" || callback === "expired" || callback === "canceled" ? callback : undefined
  const navigate = route.useNavigate()
  const queryClient = useQueryClient()
  const connections = useQuery(connectionsQuery(useEventsConnected()))
  const outcome = useQuery({
    queryKey: ["mcp-credential-login", login],
    queryFn: ({ signal }) => signInOutcome(login ?? "", signal),
    enabled: Boolean(login) && !reported,
    retry: false,
    staleTime: Infinity,
    refetchInterval: (query) => query.state.data?.status === "pending" ? 1_000 : false,
  })
  const connected = outcome.data?.status === "connected" && !reported
  useEffect(() => {
    if (connected) void queryClient.invalidateQueries({ queryKey: ["mcp-credentials"] })
  }, [connected, queryClient])
  if (connections.error) return <LoadError what="MCP connections" error={connections.error} retry={() => void connections.refetch()} />
  if (!connections.data) return <RowSkeleton lines={2} />
  const remote = connections.data.credentials.filter((connection) => connection.match)
  const servers = [...new Set(remote.map((connection) => connection.match))]
  const local = connections.data.credentials.filter((connection) => !connection.match)
  const targetID = outcome.data?.credential_id ?? connection ?? (reported === "expired" ? returning : undefined)
  const target = remote.find((credential) => credential.id === targetID && credential.kind === "oauth")
  const reason = reported ?? (outcome.error instanceof ApiError && outcome.error.status === 404 ? "expired" : undefined)
  const feedback: LoginFeedback | undefined =
    reason ? { message: callbackMessages[reason], retry: true }
      : connected ? { message: "Signed in on the mesh." }
        : outcome.data?.status === "failed" || outcome.error ? { message: "Sign-in didn't complete.", retry: true }
          : login ? { message: "Finishing sign-in…", pending: true } : undefined
  function dismissFeedback() {
    try {
      sessionStorage.removeItem(returningConnectionKey)
    } catch {
      // The row remains addressable through the return URL when storage is off.
    }
    void navigate({ replace: true, search: (previous) => ({ ...previous, callback: undefined, oauth_login: undefined, connection: undefined }) })
  }
  return (
    <Pane title="Connections">
      <div className="flex flex-col gap-(--s7)">
        {feedback && !target && <Banner tone={feedback.retry ? "warn" : "info"} action={
          <div className="flex flex-wrap gap-(--s2)">
            {feedback.retry && <Button size="sm" onClick={() => { dismissFeedback(); setAdding("") }}>Try again</Button>}
            <Button size="sm" variant="ghost" onClick={dismissFeedback}>{feedback.pending ? "Dismiss" : "Done"}</Button>
          </div>
        }>{feedback.message}</Banner>}
        {connections.data.credentials.length === 0 && (
          <Empty title="No connections">Add a server below, or run <code>agentpack mcp login</code>.</Empty>
        )}
        {servers.map((match) => <ServerGroup key={match} match={match} logins={loginsFor(match, remote)} onAdd={() => setAdding(match)} targetID={target?.id} feedback={feedback} dismissFeedback={dismissFeedback} />)}
        {local.length > 0 && (
          <FormGroup title="Local servers">
            <ul>{local.map((connection) => <ConnectionRow key={connection.id} connection={connection} single />)}</ul>
          </FormGroup>
        )}
        <div><Button onClick={() => setAdding("")}><Plus aria-hidden className="size-3.5" />Add connection…</Button></div>
        <Sheet open={adding !== undefined} onOpenChange={(open) => !open && setAdding(undefined)}>
          {adding !== undefined && <AddConnection key={adding} initialURL={adding} remote={remote} callbackURL={connections.data.callbackURL} onDone={() => setAdding(undefined)} />}
        </Sheet>
        {connections.data.credentials.some((connection) => connection.kind === "oauth") && <RevokeAll />}
      </div>
    </Pane>
  )
}

function ServerGroup({ match, logins, onAdd, targetID, feedback, dismissFeedback }: { match: string; logins: Connection[]; onAdd: () => void; targetID?: string; feedback?: LoginFeedback; dismissFeedback: () => void }) {
  const name = serverName(match)
  const oauth = logins.some((connection) => connection.kind === "oauth")
  return (
    <FormGroup
      title={<>{name}<span className="font-normal text-(--fg3)"> · {new URL(match).host}</span></>}
      action={oauth && <Button size="sm" onClick={onAdd}><Plus aria-hidden className="size-3.5" />Add login…</Button>}
      footer={logins.length > 1 && "The first login added is the default. Pick another for a project in New session."}
    >
      <ul>{logins.map((connection, index) => (
        <ConnectionRow key={connection.id} connection={connection} single={logins.length === 1} first={index === 0} feedback={targetID === connection.id ? feedback : undefined} dismissFeedback={dismissFeedback} />
      ))}</ul>
    </FormGroup>
  )
}

function ConnectionRow({ connection, single, first = false, feedback, dismissFeedback }: { connection: Connection; single: boolean; first?: boolean; feedback?: LoginFeedback; dismissFeedback?: () => void }) {
  const queryClient = useQueryClient()
  const [removing, setRemoving] = useState(false)
  const [revoking, setRevoking] = useState("")
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
    mutationFn: () => openConnectionsLogin({ url: connection.match, label: connection.label }, connection.id),
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
        {feedback && <div className="mt-(--s2) flex flex-wrap items-center gap-(--s2) text-(length:--fs-sm)">
          <span role={feedback.retry ? "alert" : "status"}>{feedback.message}</span>
          {feedback.retry && <Button size="sm" disabled={reconnect.isPending} onClick={() => reconnect.mutate()}>Try again</Button>}
          <Button size="sm" variant="ghost" onClick={dismissFeedback}>{feedback.pending ? "Dismiss" : "Done"}</Button>
        </div>}
      </div>
      {problem && connection.kind === "oauth" && !feedback && (
        <Button size="sm" variant="primary" disabled={reconnect.isPending} onClick={() => reconnect.mutate()}>Reconnect…</Button>
      )}
      <Menu>
        <MenuTrigger render={<RowMoreButton aria-label={`${connection.label} actions`} className="opacity-100 data-popup-open:opacity-100" />} />
        <MenuContent align="end">
          {connection.kind === "oauth" && <MenuItem onClick={() => reconnect.mutate()}>Reconnect…</MenuItem>}
          {connection.plugins.length > 0 && <DropdownMenuSeparator className="mx-[11px] my-[5px]" />}
          {connection.plugins.map((module) => (
            <MenuItem key={module} onClick={() => setRevoking(module)}>{`Revoke ${module.split("/").pop()}…`}</MenuItem>
          ))}
          <DropdownMenuSeparator className="mx-[11px] my-[5px]" />
          <MenuItem danger onClick={() => setRemoving(true)}>Remove…</MenuItem>
        </MenuContent>
      </Menu>
      <ConfirmAlert open={Boolean(revoking)} onOpenChange={(open) => !open && setRevoking("")}>
        {revoking && <ConfirmAlertContent
          title={`Revoke ${revoking.split("/").pop()}?`}
          description={`Remove its approval to use ${connection.label} on every machine. You can approve it again the next time a project requests this login.`}
          destructiveLabel="Revoke approval"
          onConfirm={() => { revoke.mutate(revoking); setRevoking("") }}
        />}
      </ConfirmAlert>
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
    <FormGroup title="Removed a machine?" footer="Rotate token headers and variables at their providers.">
      <FormRow label="Sign out of all OAuth logins" description="A machine that left the mesh keeps the logins it received. Sign out, then reconnect the ones you use.">
        <Button variant="destructive" disabled={revoke.isPending} onClick={() => setConfirming(true)}>Sign out of all…</Button>
      </FormRow>
      <ConfirmAlert open={confirming} onOpenChange={setConfirming}>
        {confirming && <ConfirmAlertContent
          title="Sign out of all MCP logins?"
          description="Sign out of this account’s OAuth logins on every machine and revoke their tokens where the provider supports it. Reconnect each login before agents can use it again."
          destructiveLabel="Sign out everywhere"
          onConfirm={() => { setConfirming(false); revoke.mutate() }}
        />}
      </ConfirmAlert>
    </FormGroup>
  )
}

type Method = "oauth" | "header" | "env"

function AddConnection({ callbackURL, initialURL, remote, onDone }: { callbackURL: string; initialURL: string; remote: Connection[]; onDone: () => void }) {
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
      if (method === "oauth") return openConnectionsLogin({ url, label, client_id: clientID, client_secret: clientSecret })
      if (method === "env") await api("/api/mcp/credentials", { body: { kind: "env", label, values: { [variable]: value } } })
      else await api("/api/mcp/credentials", { body: { kind: "header", url, label, values: { [header]: value } } })
    },
    onSuccess: () => {
      if (method === "oauth") return
      hud.success(method === "env" ? `Saved ${variable} as ${label}. Local servers use it with credential = "${label}".` : `Saved the ${header} header for every machine.`)
      void queryClient.invalidateQueries({ queryKey: ["mcp-credentials"] })
      onDone()
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
    <SheetContent>
      <SheetTitle>{initialURL ? "Add login" : "Add connection"}</SheetTitle>
      <SheetDescription>{initialURL ? `Create another sign-in for ${serverName(initialURL)}.` : "Choose how agents on the mesh can use this server."}</SheetDescription>
      <form className="flex flex-col gap-(--s4)" onSubmit={submit}>
        <FieldGrid>
          <Field>
            <FieldLabel htmlFor="mcp-label">Alias</FieldLabel>
            <Input id="mcp-label" required={method === "env"} autoFocus={Boolean(initialURL)} maxLength={256}
              placeholder={method === "env" ? "Name used by local servers" : method === "oauth" && taken.length > 0 ? "e.g. Client workspace" : "Defaults to the host"} value={label} onChange={(event) => setLabel(event.target.value)} />
          </Field>
          {initialURL ? <p className="text-(length:--fs-sm) text-(--fg2)">OAuth sign-in</p> : (
            <SegmentedControl aria-label="Connection method" value={[method]} onValueChange={(values) => {
              if (values[0] === "oauth" || values[0] === "header" || values[0] === "env") setMethod(values[0])
            }}>
              <SegmentedControlItem value="oauth">Sign in</SegmentedControlItem>
              <SegmentedControlItem value="header">Token header</SegmentedControlItem>
              <SegmentedControlItem value="env">Variable</SegmentedControlItem>
            </SegmentedControl>
          )}
          {method !== "env" && (
            <Field>
              <FieldLabel htmlFor="mcp-url">Server URL</FieldLabel>
              <Input id="mcp-url" type="url" required readOnly={Boolean(initialURL)} placeholder="https://api.githubcopilot.com/mcp/" value={url} onChange={(event) => setURL(event.target.value)} />
            </Field>
          )}
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
            <details className="space-y-(--s3) text-(length:--fs-sm)">
              <summary className="cursor-pointer text-(--fg2)">Advanced OAuth settings</summary>
              <Field>
                <FieldLabel htmlFor="mcp-client">Client ID</FieldLabel>
                <Input id="mcp-client" placeholder="Optional. Otherwise registered automatically." value={clientID} onChange={(event) => setClientID(event.target.value)} />
              </Field>
              {clientID && (
                <Field>
                  <FieldLabel htmlFor="mcp-client-secret">Client secret</FieldLabel>
                  <Input id="mcp-client-secret" type="password" autoComplete="off" placeholder="Only for confidential clients" value={clientSecret} onChange={(event) => setClientSecret(event.target.value)} />
                </Field>
              )}
              {clientID && callbackURL && <p className="break-all text-(--fg2)">Register <code>{callbackURL}</code> as the redirect URL with the OAuth app.</p>}
            </details>
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
        <SheetActions>
          <Button type="submit" variant="primary" disabled={add.isPending}>{add.isPending ? method === "oauth" ? "Opening sign-in…" : "Saving…" : method === "oauth" ? "Sign in…" : "Save"}</Button>
        </SheetActions>
      </form>
    </SheetContent>
  )
}
