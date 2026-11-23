import { useEffect, useRef, useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { GroupHeader } from "@/components/kit/list"
import { Field, FieldError, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { SheetActions, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import type { PackageItem } from "@/features/library/model"
import { packagesQuery } from "@/features/library/queries"
import { useLibrary } from "@/features/library/use-library"
import { connectionsQuery, loginsFor, startSignIn, waitForSignIn, type Connection } from "@/features/settings/connections-model"
import { api, ApiError, errorText, LOCAL, routeMissing } from "@/lib/api"
import type { MCPServerInput, ProjectMCP } from "@/lib/api-types"
import { hud } from "@/lib/hud"
import type { ListedSession } from "./group"
import { projectDirectory } from "./git"
import { projectsQuery, sessionsQuery } from "./queries"

const section = "flex flex-col gap-(--s3)"
function loginURL(value: string) {
  try {
    const url = new URL(value)
    if ((url.protocol === "http:" || url.protocol === "https:") && !url.username && !url.password && !url.search) {
      return url.origin + url.pathname.replace(/\/+$/, "")
    }
  } catch {
    // A URL being typed is not ready for sign-in.
  }
}

const itemRow = "flex min-w-0 flex-wrap items-center justify-between gap-(--s3) border-t border-(--sep-soft) py-(--s2)"

type PluginLogin = { module: string; url: string; credential: string }

export function ProjectSettings({ row, onDone }: { row: ListedSession; onDone: () => void }) {
  const { machine } = row
  const directory = projectDirectory(row)
  const queryClient = useQueryClient()
  const projects = useQuery(projectsQuery(machine))
  const refreshProjects = () => queryClient.invalidateQueries({ queryKey: projectsQuery(machine).queryKey })
  const project = projects.data?.find((item) => item.directory === directory)
  const connections = useQuery(connectionsQuery(false))
  const credentials = connections.data?.credentials ?? []
  const library = useLibrary()
  const [search, setSearch] = useState("")
  const [spec, setSpec] = useState("")
  const [add, setAdd] = useState<Record<string, string>>({})
  const [remove, setRemove] = useState<string[]>([])
  const [removeMCP, setRemoveMCP] = useState<string[]>([])
  const [logins, setLogins] = useState<Record<string, string>>({})
  const [pluginLogins, setPluginLogins] = useState<Record<string, PluginLogin>>({})
  const [mcpEnabled, setMCPEnabled] = useState<Record<string, boolean>>({})
  const [edit, setEdit] = useState<MCPServerInput>()
  const [editingExisting, setEditingExisting] = useState(false)
  const [servers, setServers] = useState<MCPServerInput[]>([])
  const [args, setArgs] = useState("")
  const [loginError, setLoginError] = useState("")
  const [connecting, setConnecting] = useState(false)
  const signInRequest = useRef<AbortController | null>(null)
  const justConnected = useRef(false)
  useEffect(() => () => signInRequest.current?.abort(), [])
  const editedServer: MCPServerInput | undefined = edit && {
    name: edit.name.trim(),
    ...(edit.command !== undefined
      ? { command: edit.command.trim(), args: args.split("\n").filter((arg) => arg.trim()) }
      : { url: edit.url?.trim(), type: edit.type }),
  }
  const savedServers = editedServer
    ? [...servers.filter((server) => server.name !== editedServer.name), editedServer]
    : servers
  // An explicit login saved before plugin login selection used the URL alone.
  const savedPluginLogins = { ...pluginLogins }
  for (const server of project?.mcp ?? []) {
    if (server.source === "plugin" && server.module && server.url && server.login && !server.project_login &&
        !savedPluginLogins[server.name] && (mcpEnabled[server.name] ?? !server.disabled) &&
        !savedServers.some((edit) => edit.name === server.name) &&
        loginsFor(server.url, credentials).some((login) => login.id === server.login)) {
      savedPluginLogins[server.name] = { module: server.module, url: server.url, credential: server.login }
    }
  }
  const save = useMutation({
    mutationFn: () => api("/api/projects/settings", {
      machine,
      method: "PUT",
      body: {
        directory,
        add: [...Object.values(add), ...(spec.trim() ? [spec.trim()] : [])],
        remove,
        remove_mcp: removeMCP.filter((name) => !savedServers.some((server) => server.name === name)),
        servers: savedServers,
        logins,
        ...(Object.keys(savedPluginLogins).length > 0 && { plugin_logins: savedPluginLogins }),
        ...(Object.keys(mcpEnabled).length > 0 && {
          mcp_enabled: Object.fromEntries(Object.entries(mcpEnabled).filter(([name]) => !removeMCP.includes(name))),
        }),
      },
    }),
    // A new credential can reach the project's machine just after the browser's OAuth callback.
    retry: (count, error) => justConnected.current && machine !== LOCAL && count < 3 &&
      error instanceof ApiError && error.status === 404 && !routeMissing(error),
    retryDelay: 1500,
    onSuccess: async () => {
      await Promise.all([
        refreshProjects(),
        queryClient.invalidateQueries({ queryKey: packagesQuery(machine).queryKey }),
        queryClient.invalidateQueries({ queryKey: sessionsQuery(false).queryKey }),
      ])
      hud.success("Project settings saved")
      onDone()
    },
    onError: () => void refreshProjects(),
  })
  const busy = save.isPending || connecting
  const changed = Boolean(Object.keys(add).length || spec.trim() || remove.length || removeMCP.length || savedServers.length || Object.keys(logins).length || Object.keys(savedPluginLogins).length || Object.keys(mcpEnabled).length)
  const installed = project?.packages ?? []
  const editURL = loginURL(edit?.url ?? "")
  const choices = library.items.filter((item): item is PackageItem =>
    (item.type === "skill" || item.type === "plugin") &&
    !installed.some((pkg) => pkg.module === item.module && pkg.commit === item.commit) &&
    `${item.name} ${item.module}`.toLowerCase().includes(search.trim().toLowerCase()))
  const toggle = (values: string[], value: string, checked: boolean) => checked ? [...values, value] : values.filter((item) => item !== value)

  function submit(event: FormEvent) {
    event.preventDefault()
    if (project?.available && changed && !busy) save.mutate()
  }

  async function connect(url: string, label: string, selectLogin: (id: string) => void) {
    const tab = window.open("about:blank", "_blank")
    if (!tab) {
      setLoginError("Allow pop-ups to sign in without losing your edits.")
      return
    }
    tab.opener = null
    const request = new AbortController()
    signInRequest.current = request
    setLoginError("")
    setConnecting(true)
    try {
      const started = await startSignIn({ url, label })
      request.signal.throwIfAborted()
      tab.location.href = started.authorize_url
      const id = await waitForSignIn(started.login, request.signal)
      await connections.refetch({ throwOnError: true })
      if (!request.signal.aborted) {
        justConnected.current = true
        selectLogin(id)
      }
    } catch (error) {
      if (!request.signal.aborted) setLoginError(errorText(error))
    }
    tab.close()
    if (!request.signal.aborted) setConnecting(false)
  }

  function loginControl(name: string, url: string, server?: ProjectMCP) {
    const pending = pluginLogins[name]
    const plugin = server?.source === "plugin"
    const outdated = plugin && !server.module
    return <ProjectLogin name={name} url={url}
      current={plugin ? pending?.credential ?? server.login : logins[url] ?? server?.login}
      credentials={credentials} plugin={plugin} outdated={outdated}
      busy={busy} canConnect={connections.isSuccess} connect={connect}
      onChange={(value) => {
        if (plugin) {
          const module = server.module
          if (module) setPluginLogins((current) => ({ ...current, [name]: { module, url, credential: value } }))
        } else {
          setLogins((current) => ({ ...current, [url]: value }))
        }
      }} />
  }

  function openEditor(server: MCPServerInput, existing: boolean) {
    setEdit(server)
    setEditingExisting(existing)
    setArgs(server.args?.join("\n") ?? "")
  }

  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>Project settings</SheetTitle>
      <SheetDescription className="break-all">{row.definition_root || directory} · {row.machineName}</SheetDescription>
      <div className="min-h-0 space-y-(--s5) overflow-y-auto">
        {projects.isPending && <p role="status">Loading project…</p>}
        {projects.isError && <FieldError>{errorText(projects.error)}</FieldError>}
        {!projects.isPending && !projects.isError && !project && <FieldError>Project folder unavailable.</FieldError>}
        {project && !project.available && <FieldError>{project.unavailable_reason || "Project folder unavailable."}</FieldError>}
        {project?.error && <FieldError>{project.error}</FieldError>}
        {project?.available && !project.error && (
          <>
            <section aria-label="Packages" className={section}>
              <GroupHeader>Packages</GroupHeader>
              {installed.length === 0 && <p className="text-(--fg2)">No packages installed.</p>}
              {installed.map((pkg) => (
                <div key={pkg.module} className={itemRow}>
                  <span className="min-w-0 break-all">{pkg.name || pkg.module}</span>
                  {pkg.direct ? (
                    <label className="flex items-center gap-(--s2) text-(--fg2)">
                      <Checkbox disabled={busy} checked={remove.includes(pkg.module)} onCheckedChange={(checked) => setRemove(toggle(remove, pkg.module, checked))} />
                      Remove <span className="sr-only">{pkg.name || pkg.module}</span>
                    </label>
                  ) : <span className="text-(--fg2)">Dependency</span>}
                </div>
              ))}
              <Input aria-label="Search Library packages" placeholder="Search Library packages" value={search} onChange={(event) => setSearch(event.target.value)} />
              <div role="group" aria-label="Library packages" className="max-h-44 overflow-y-auto">
                {choices.map((pkg) => (
                  <label key={pkg.key} className={itemRow}>
                    <span className="min-w-0 flex-1 break-all">{pkg.name} <span className="text-(--fg2)">{pkg.module}</span></span>
                    <Checkbox
                      aria-label={`Add ${pkg.name}`}
                      disabled={busy}
                      checked={Boolean(add[pkg.module])}
                      onCheckedChange={(checked) => setAdd((current) => {
                        const next = { ...current }
                        if (checked) next[pkg.module] = `${pkg.module}@${pkg.commit}`
                        else delete next[pkg.module]
                        return next
                      })}
                    />
                  </label>
                ))}
                {choices.length === 0 && <p className="text-(--fg2)">{library.loading ? "Loading Library…" : "No matching packages."}</p>}
              </div>
              <Field>
                <FieldLabel htmlFor="project-package">Package source</FieldLabel>
                <Input id="project-package" placeholder="module@commit" disabled={busy} value={spec} onChange={(event) => setSpec(event.target.value)} />
              </Field>
            </section>
            <section aria-label="MCP servers" className={section}>
              <GroupHeader>MCP servers</GroupHeader>
              {connections.isError && <FieldError>{errorText(connections.error)}</FieldError>}
              {project.mcp.length === 0 && <p className="text-(--fg2)">No MCP servers configured.</p>}
              {project.mcp.filter((server) => !servers.some((draft) => draft.name === server.name)).map((server) => {
                const url = server.url
                const enabled = mcpEnabled[server.name] ?? !server.disabled
                return (
                  <div key={server.name} className={itemRow}>
                    <label className="flex min-w-0 flex-1 items-center gap-(--s3)">
                      <Checkbox checked={enabled} disabled={busy || removeMCP.includes(server.name)} aria-label={`Enable ${server.name}`} onCheckedChange={(checked) => setMCPEnabled((current) => {
                        const next = { ...current }
                        if (checked === !server.disabled) delete next[server.name]
                        else next[server.name] = checked
                        return next
                      })} />
                      <span className="min-w-0 break-all">{server.name} <span className="text-(--fg2)">{server.host || server.launcher}</span></span>
                    </label>
                    {url && enabled && !removeMCP.includes(server.name) && loginControl(server.name, url, server)}
                    {server.source === "manifest" && (
                      <>
                        {url && <Button type="button" size="sm" disabled={busy || Boolean(edit)} onClick={() => openEditor({ name: server.name, url, type: server.transport }, true)}>Edit<span className="sr-only"> {server.name}</span></Button>}
                        <label className="flex items-center gap-(--s2) text-(--fg2)">
                          <Checkbox disabled={busy || edit?.name === server.name} checked={removeMCP.includes(server.name)} onCheckedChange={(checked) => setRemoveMCP(toggle(removeMCP, server.name, checked))} />
                          Remove <span className="sr-only">{server.name}</span>
                        </label>
                      </>
                    )}
                  </div>
                )
              })}
              {servers.map((server) => {
                const url = loginURL(server.url ?? "")
                return (
                  <div className={itemRow} key={server.name}>
                    <span className="min-w-0 flex-1 break-all">{server.name} <span className="text-(--fg2)">· Unsaved</span></span>
                    {url && loginControl(server.name, url)}
                    <Button type="button" size="sm" disabled={busy || Boolean(edit)} onClick={() => openEditor(server, true)}>Edit draft</Button>
                    <Button type="button" size="sm" disabled={busy || edit?.name === server.name}
                      onClick={() => setServers(servers.filter((item) => item.name !== server.name))}>Discard draft</Button>
                  </div>
                )
              })}
              {edit ? (
                <>
                  <Field>
                    <FieldLabel htmlFor="project-mcp-name">Server name</FieldLabel>
                    <Input id="project-mcp-name" required disabled={busy || editingExisting} value={edit.name} onChange={(event) => setEdit({ ...edit, name: event.target.value })} />
                  </Field>
                  <Field>
                    <FieldLabel>Transport</FieldLabel>
                    <PopupSelect label="MCP transport" disabled={busy}
                      value={edit.command !== undefined ? "stdio" : edit.type || "http"}
                      items={[{ value: "http", label: "HTTP" }, { value: "sse", label: "SSE" }, { value: "stdio", label: "Command" }]}
                      onValueChange={(type) => setEdit(type === "stdio" ? { name: edit.name, command: "" } : { name: edit.name, type, url: edit.url ?? "" })}
                    />
                  </Field>
                  {edit.command !== undefined ? (
                    <>
                      <Field>
                        <FieldLabel htmlFor="project-mcp-command">Command</FieldLabel>
                        <Input id="project-mcp-command" required disabled={busy} value={edit.command} onChange={(event) => setEdit({ ...edit, command: event.target.value })} />
                      </Field>
                      <Field>
                        <FieldLabel htmlFor="project-mcp-args">Arguments</FieldLabel>
                        <textarea id="project-mcp-args" className="min-w-0 flex-1 rounded-(--r-ctl) border border-(--sep) bg-(--control) p-(--s2)" placeholder="One argument per line" disabled={busy} value={args} onChange={(event) => setArgs(event.target.value)} />
                      </Field>
                    </>
                  ) : (
                    <Field>
                      <FieldLabel htmlFor="project-mcp-url">Server URL</FieldLabel>
                      <Input id="project-mcp-url" type="url" required disabled={busy} value={edit.url ?? ""} onChange={(event) => setEdit({ ...edit, url: event.target.value })} />
                    </Field>
                  )}
                  {editURL && <Field>
                    <FieldLabel>Login</FieldLabel>
                    {loginControl(edit.name || "New MCP server", editURL, project.mcp.find((server) => server.name === edit.name && server.url === editURL && server.source === "manifest"))}
                  </Field>}
                  <div className="flex flex-wrap gap-(--s3)">
                    <Button type="button" size="sm" disabled={busy || !editedServer?.name || !(editedServer.url || editedServer.command)} onClick={() => {
                      setServers(savedServers)
                      setEdit(undefined)
                    }}>Keep server edit</Button>
                    <Button type="button" size="sm" disabled={busy} onClick={() => setEdit(undefined)}>Discard server edit</Button>
                  </div>
                </>
              ) : <Button type="button" size="sm" className="self-start" disabled={busy} onClick={() => openEditor({ name: "", url: "" }, false)}>Add MCP server…</Button>}
              {loginError && <FieldError>{loginError}</FieldError>}
              {connecting && <div className="flex items-center gap-(--s3)">
                <p role="status" className="text-(--fg2)">Waiting for sign-in…</p>
                <Button type="button" size="sm" onClick={() => {
                  signInRequest.current?.abort()
                  setConnecting(false)
                }}>Cancel sign-in</Button>
              </div>}
            </section>
            <p className="text-(length:--fs-sm) text-(--fg2)">Affected agents reload when idle. New sessions use the saved settings.</p>
          </>
        )}
      </div>
      {save.error && <FieldError>{routeMissing(save.error) ? `Update Agentpack on ${row.machineName} to save project settings.` : errorText(save.error)}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!project?.available || Boolean(project.error) || !changed || busy}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </SheetActions>
    </form>
  )
}

function ProjectLogin({ name, url, current, credentials, plugin, outdated, busy, canConnect, connect, onChange }: {
  name: string
  url: string
  current?: string
  credentials: Connection[]
  plugin: boolean
  outdated: boolean
  busy: boolean
  canConnect: boolean
  connect: (url: string, label: string, selectLogin: (id: string) => void) => Promise<void>
  onChange: (value: string) => void
}) {
  const logins = loginsFor(url, credentials)
  const chosen = logins.find((login) => login.id === current)
  const missing = Boolean(current && !chosen && canConnect)
  const active = missing ? undefined : chosen ?? (plugin ? undefined : logins[0])
  function signIn() {
    const labels = new Set(logins.map((login) => login.label.toLowerCase()))
    let label = name
    for (let number = 2; labels.has(label.toLowerCase()); number++) label = `${name} ${number}`
    void connect(url, active?.label ?? label, onChange)
  }
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-(--s2)">
      {logins.length > 0 && (
        <PopupSelect label={`${name} login`} value={missing ? "missing" : chosen?.id ?? "default"} disabled={busy || outdated}
          items={[
            ...(missing ? [{ value: "missing", label: "Selected login unavailable", disabled: true }] : []),
            plugin ? { value: "default", label: "Choose login", disabled: true } : { value: "default", label: `Default · ${logins[0]!.label}` },
            ...logins.map((login) => ({ value: login.id, label: login.status === "needs_login" ? `${login.label} · Needs sign-in` : login.label })),
          ]}
          onValueChange={(value) => onChange(value === "default" ? "" : value)}
        />
      )}
      {canConnect && !outdated && (!active || active.status === "needs_login") && (
        <Button type="button" size="sm" disabled={busy} onClick={signIn} title="Opens in a new tab">
          {active ? "Reconnect…" : "Sign in…"}
        </Button>
      )}
      {outdated && <span className="text-(length:--fs-xs) text-(--fg2)">Update this machine to allow plugin logins.</span>}
      {missing && logins.length === 0 && <span className="text-(length:--fs-xs) text-(--warn-word)">Selected login unavailable.</span>}
    </div>
  )
}
