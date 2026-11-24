import { useRef, useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { GroupHeader } from "@/components/kit/list"
import { Field, FieldError, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { SheetActions, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import type { PackageItem } from "@/features/library/model"
import { packagesQuery } from "@/features/library/queries"
import { useLibrary } from "@/features/library/use-library"
import { connectionsQuery, loginsFor, type Connection } from "@/features/settings/connections-model"
import { api, ApiError, errorText, LOCAL, routeMissing } from "@/lib/api"
import type { MCPServerInput, ProjectMCP } from "@/lib/api-types"
import { hud } from "@/lib/hud"
import type { ListedSession } from "./group"
import { projectDirectory } from "./git"
import { ProjectSignInStatus, useProjectSignIn } from "./project-sign-in"
import { projectsQuery, selectedProjectQuery, sessionsQuery } from "./queries"

const section = "flex flex-col gap-(--s2)"
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

// One row shape for packages and servers: name leads, every control trails, one left edge.
const itemRow = "flex min-h-(--row-h) min-w-0 flex-wrap items-center gap-(--s4) border-t-[0.5px] border-(--sep-soft) py-(--s2) first-of-type:border-t-0 data-off:[&>span:first-child]:line-through data-off:[&>span:first-child]:opacity-50"

type PluginLogin = { module: string; url: string; credential: string }

export function ProjectSettings({ row, onDone }: { row: ListedSession; onDone: () => void }) {
  const { machine } = row
  const directory = projectDirectory(row)
  const queryClient = useQueryClient()
  const projects = useQuery(projectsQuery(machine))
  const summary = projects.data?.find((item) => item.directory === directory)
  const details = useQuery({
    ...selectedProjectQuery(machine, directory),
    enabled: summary?.details_deferred === true,
  })
  const active = summary?.details_deferred ? details : projects
  const refreshProjects = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: projectsQuery(machine).queryKey }),
    queryClient.invalidateQueries({ queryKey: selectedProjectQuery(machine, directory).queryKey }),
  ])
  const project = summary?.details_deferred ? details.data : summary
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
  const signIn = useProjectSignIn(() => connections.refetch({ throwOnError: true }))
  const justConnected = useRef(false)
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
  const removedServers = removeMCP.filter((name) => !savedServers.some((server) => server.name === name))
  const enabledChanges = Object.entries(mcpEnabled).filter(([name]) => !removeMCP.includes(name))
  const save = useMutation({
    mutationFn: () => api("/api/projects/settings", {
      machine,
      method: "PUT",
      body: {
        directory,
        add: [...Object.values(add), ...(spec.trim() ? [spec.trim()] : [])],
        remove,
        remove_mcp: removedServers,
        servers: savedServers,
        logins,
        ...(Object.keys(savedPluginLogins).length > 0 && { plugin_logins: savedPluginLogins }),
        ...(enabledChanges.length > 0 && { mcp_enabled: Object.fromEntries(enabledChanges) }),
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
  const busy = save.isPending || Boolean(signIn.pending)
  const installed = project?.packages ?? []
  const loginChange = (name: string, id: string) => `Use ${id ? credentials.find((item) => item.id === id)?.label ?? "the selected login" : "the default login"} for ${name}`
  const changes = [
    ...Object.keys(add).map((module) => `Add ${library.items.find((item) => (item.type === "skill" || item.type === "plugin") && item.module === module)?.name || module}`),
    ...(spec.trim() ? [`Add ${spec.trim()}`] : []),
    ...remove.map((module) => `Remove ${installed.find((pkg) => pkg.module === module)?.name || module}`),
    ...removedServers.map((name) => `Remove ${name}`),
    ...savedServers.map((server) => `${project?.mcp.some((item) => item.name === server.name) ? "Edit" : "Add"} ${server.name || "MCP server"}`),
    ...enabledChanges.map(([name, enabled]) => `${enabled ? "Enable" : "Disable"} ${name}`),
    ...Object.entries(logins).map(([url, id]) => loginChange(savedServers.find((server) => server.url === url)?.name ?? project?.mcp.find((server) => server.url === url)?.name ?? url, id)),
    ...Object.entries(savedPluginLogins).map(([name, login]) => loginChange(name, login.credential)),
  ]
  const changed = changes.length > 0
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

  function loginControl(name: string, url: string, server?: ProjectMCP) {
    const pending = pluginLogins[name]
    const plugin = server?.source === "plugin"
    const outdated = plugin && !server.module
    return <ProjectLogin name={name} url={url}
      current={plugin ? pending?.credential ?? server.login : logins[url] ?? server?.login}
      credentials={credentials} plugin={plugin} outdated={outdated}
      busy={busy} canConnect={connections.isSuccess}
      connect={(name, url, label, onConnected) => signIn.connect(name, url, label, (id) => {
        justConnected.current = true
        onConnected(id)
      })}
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
      <header className="sticky -top-(--dlg-pad) z-10 -mx-(--dlg-pad) -mt-(--dlg-pad) border-b-[0.5px] border-(--line) bg-(--raised) px-(--dlg-pad) pt-(--dlg-pad) pb-(--s4)">
        <SheetTitle>Project settings</SheetTitle>
        <SheetDescription className="wrap-anywhere">{row.machineName} · {directory}</SheetDescription>
      </header>
      <div className="flex flex-col gap-(--s8)">
        {active.isPending && <p role="status">Loading project…</p>}
        {active.isError && <FieldError>{errorText(active.error)}</FieldError>}
        {!active.isPending && !active.isError && !project && <FieldError>Project folder unavailable.</FieldError>}
        {project && !project.available && <FieldError>{project.unavailable_reason || "Project folder unavailable."}</FieldError>}
        {project?.error && <FieldError>{project.error}</FieldError>}
        {project?.available && !project.error && (
          <>
            <section aria-label="Packages" className={section}>
              <GroupHeader className="px-0">Packages</GroupHeader>
              {installed.length === 0 && <p className="text-(--fg2)">No packages installed.</p>}
              {installed.map((pkg) => (
                <div key={pkg.module} className={itemRow} data-off={remove.includes(pkg.module) || undefined}>
                  <span className="min-w-0 flex-1 truncate" title={pkg.module}>{pkg.name || pkg.module}</span>
                  {pkg.direct ? (
                    <Button type="button" size="sm" variant={remove.includes(pkg.module) ? "default" : "ghost"} disabled={busy} onClick={() => setRemove(toggle(remove, pkg.module, !remove.includes(pkg.module)))}>
                      {remove.includes(pkg.module) ? "Undo" : "Remove"}<span className="sr-only"> {pkg.name || pkg.module}</span>
                    </Button>
                  ) : <span className="text-(length:--fs-sm) text-(--fg2)">Dependency</span>}
                </div>
              ))}
              <GroupHeader className="mt-(--s5) px-0">Add from Library</GroupHeader>
              <Input aria-label="Search Library packages" placeholder="Search Library packages" value={search} onChange={(event) => setSearch(event.target.value)} />
              <div role="group" aria-label="Library packages">
                {choices.map((pkg) => (
                  <div key={pkg.key} className={itemRow}>
                    <span className="min-w-0 flex-1 truncate" title={pkg.module}>{pkg.name} <span className="text-(length:--fs-sm) text-(--fg2)">{pkg.module}</span></span>
                    <Button type="button" size="sm" variant={add[pkg.module] ? "default" : "ghost"} disabled={busy}
                      onClick={() => setAdd((current) => {
                        const next = { ...current }
                        if (next[pkg.module]) delete next[pkg.module]
                        else next[pkg.module] = `${pkg.module}@${pkg.commit}`
                        return next
                      })}>
                      {add[pkg.module] ? "Added ✓" : "Add"}<span className="sr-only"> {pkg.name}</span>
                    </Button>
                  </div>
                ))}
                {choices.length === 0 && <p className="text-(--fg2)">{library.loading ? "Loading Library…" : "No matching packages."}</p>}
              </div>
              <Field>
                <FieldLabel htmlFor="project-package" className="w-auto">Or add by source</FieldLabel>
                <Input id="project-package" placeholder="github.com/owner/repo/path@ref" disabled={busy} value={spec} onChange={(event) => setSpec(event.target.value)} />
              </Field>
            </section>
            <section aria-label="MCP servers" className={section}>
              <GroupHeader className="px-0">MCP servers</GroupHeader>
              {connections.isError && <FieldError>{errorText(connections.error)}</FieldError>}
              {project.mcp.length === 0 && <p className="text-(--fg2)">No MCP servers configured.</p>}
              {project.mcp.filter((server) => !servers.some((draft) => draft.name === server.name)).map((server) => {
                const url = server.url
                const enabled = mcpEnabled[server.name] ?? !server.disabled
                return (
                  <div key={server.name} className={itemRow} data-off={removeMCP.includes(server.name) || !enabled || undefined}>
                    <span className="min-w-40 flex-1 truncate" title={server.url || server.launcher}>{server.name} <span className="text-(length:--fs-sm) text-(--fg2)">{server.host || server.launcher}</span></span>
                    {url && enabled && !removeMCP.includes(server.name) && loginControl(server.name, url, server)}
                    {server.source === "manifest" && (
                      <>
                        {url && <Button type="button" size="sm" variant="ghost" disabled={busy || Boolean(edit)} onClick={() => openEditor({ name: server.name, url, type: server.transport }, true)}>Edit<span className="sr-only"> {server.name}</span></Button>}
                        <Button type="button" size="sm" variant={removeMCP.includes(server.name) ? "default" : "ghost"} disabled={busy || edit?.name === server.name} onClick={() => setRemoveMCP(toggle(removeMCP, server.name, !removeMCP.includes(server.name)))}>
                          {removeMCP.includes(server.name) ? "Undo" : "Remove"}<span className="sr-only"> {server.name}</span>
                        </Button>
                      </>
                    )}
                    <Switch checked={enabled} disabled={busy || removeMCP.includes(server.name)} aria-label={`Use ${server.name}`} onCheckedChange={(checked) => setMCPEnabled((current) => {
                      const next = { ...current }
                      if (checked === !server.disabled) delete next[server.name]
                      else next[server.name] = checked
                      return next
                    })} />
                  </div>
                )
              })}
              {servers.map((server) => {
                const url = loginURL(server.url ?? "")
                return (
                  <div className={itemRow} key={server.name}>
                    <span className="min-w-0 flex-1 truncate">{server.name} <span className="text-(--fg2)">· Unsaved</span></span>
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
              ) : <Button type="button" size="sm" className="mt-(--s3) self-start" disabled={busy} onClick={() => openEditor({ name: "", url: "" }, false)}>Add MCP server…</Button>}
              <ProjectSignInStatus signIn={signIn} />
            </section>
          </>
        )}
      </div>
      {(changed || signIn.pending) && (
        <section aria-label="Changes" className={section}>
          <GroupHeader className="px-0">Changes</GroupHeader>
          <ul className="list-disc ps-(--s6) text-(length:--fs-sm) text-(--fg2)">
            {changes.map((change, index) => <li className="wrap-anywhere" key={index}>{change}</li>)}
            {signIn.pending && <li className="wrap-anywhere">Sign in to {signIn.pending.name}</li>}
          </ul>
        </section>
      )}
      {save.error && <FieldError>{routeMissing(save.error) ? `Update Agentpack on ${row.machineName} to save project settings.` : errorText(save.error)}</FieldError>}
      <SheetActions status={changed ? `${changes.length} ${changes.length === 1 ? "change" : "changes"} · agents reload when idle` : "Saved settings reach agents when they are idle"}>
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
  connect: (name: string, url: string, label: string, selectLogin: (id: string) => void) => Promise<void>
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
    void connect(name, url, active?.label ?? label, onChange)
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
      {outdated && <span className="text-(length:--fs-xs) text-(--fg2)" title="Update this machine to pick a login for plugin servers.">Needs update</span>}
      {missing && logins.length === 0 && <span className="text-(length:--fs-xs) text-(--warn-word)">Selected login unavailable.</span>}
    </div>
  )
}
