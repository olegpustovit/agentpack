import { useState, type FormEvent, type ReactNode } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useRouterState } from "@tanstack/react-router"
import { Check, ChevronDown, ChevronLeft } from "lucide-react"
import { Empty, Spinner } from "@/components/kit/feedback"
import { FieldError, FormGroup } from "@/components/kit/forms"
import { SearchField } from "@/components/kit/list"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { DropdownMenuSeparator } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { installMCP, installPackage } from "@/features/library/mutations"
import type { LibraryProject, PackageItem } from "@/features/library/model"
import { catalogQuery, packagesQuery, useSettled } from "@/features/library/queries"
import { useLibrary } from "@/features/library/use-library"
import { useMachines } from "@/features/machines/queries"
import { connectionsQuery, loginsFor, serverName, type Connection } from "@/features/settings/connections-model"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { api, ApiError, errorText, LOCAL, routeMissing } from "@/lib/api"
import type { CatalogItem, MCPServerInput, ProjectMCP, ProjectPackage } from "@/lib/api-types"
import { basename, plural } from "@/lib/format"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { ProjectSignInStatus, useProjectSignIn } from "./project-sign-in"
import { projectsQuery, selectedProjectQuery, sessionsQuery } from "./queries"

// One change to PUT /api/projects/settings. The endpoint takes a batch; here every gesture sends its own.
type Patch = {
  add?: string[]
  remove?: string[]
  remove_mcp?: string[]
  servers?: MCPServerInput[]
  logins?: Record<string, string>
  plugin_logins?: Record<string, { module: string; url: string; credential: string }>
  mcp_enabled?: Record<string, boolean>
}

const without = <T,>(record: Record<string, T>, key: string) => Object.fromEntries(Object.entries(record).filter(([name]) => name !== key))
const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const shortSource = (module: string) => module.replace(/^github\.com\//i, "")
const moduleOf = (spec: string) => spec.split("@")[0]!.toLowerCase()
// A server is named for agents by the project; anything that runs it from a package manager or a container is a command.
const runner = /^(npx|bunx|uvx|uv|pnpm|yarn|bun|deno|docker|node|python3?)\s/

// A new credential can reach the project's machine just after the browser's OAuth callback, so a
// login change right after signing in retries a plain 404 (never a missing route) a few times.
async function putSettings(machine: string, directory: string, patch: Patch, afterSignIn = false): Promise<unknown> {
  const body = { directory, add: [], remove: [], remove_mcp: [], servers: [], logins: {}, ...patch }
  for (let attempt = 0; ; attempt++) {
    const result = await api("/api/projects/settings", { machine, method: "PUT", body }).then((value) => ({ value }), (error: unknown) => ({ error }))
    if (!("error" in result)) return result.value
    const retry = afterSignIn && machine !== LOCAL && attempt < 3 && result.error instanceof ApiError && result.error.status === 404 && !routeMissing(result.error)
    if (!retry) throw result.error
    await wait(1500)
  }
}

// "owner/repo", "github.com/owner/repo/path@ref" or a pasted GitHub URL. Returns the spec install accepts.
export function sourceSpec(input: string): string | undefined {
  const text = input.trim()
  if (/^https?:\/\//i.test(text) && !/^https?:\/\/(www\.)?github\.com\//i.test(text)) return undefined
  const value = text.replace(/^https?:\/\/(www\.)?/, "").replace(/\.git$/, "").replace(/\/+$/, "")
  if (!/^(github\.com\/)?[\w-]+\/[\w.-]+(\/[\w./-]+)?(@[\w./-]+)?$/.test(value)) return undefined
  return value.startsWith("github.com/") ? value : `github.com/${value}`
}

// What one pasted line adds: a URL is an HTTP server (SSE when its path ends in /sse), anything else a command
// and its arguments. The name comes from the host or the package, unless one is given, and never repeats one in use.
export function serverFrom(input: string, name: string, taken: string[]): MCPServerInput | undefined {
  const text = input.trim()
  let server: MCPServerInput
  if (/^https?:\/\//i.test(text)) {
    if (!URL.canParse(text)) return undefined
    // shortcut: transport is guessed from the path (SSE is deprecated in the MCP spec); add a choice if a server breaks it.
    server = { name: serverName(text).toLowerCase(), url: text, type: /\/sse\/?$/i.test(new URL(text).pathname) ? "sse" : "http" }
  } else {
    const [command, ...args] = (text.match(/"[^"]*"|'[^']*'|\S+/g) ?? []).map((part) => part.replace(/^(["'])(.*)\1$/, "$2"))
    if (!command) return undefined
    const pkg = args.find((arg) => /[/@]|mcp/i.test(arg)) ?? args.find((arg) => !arg.startsWith("-")) ?? command
    const clean = (part = "") => part.replace(/^@/, "").replace(/\.(js|ts|py)$/, "").replace(/^(mcp-server-|server-|mcp-)|(-mcp-server|-server|-mcp)$/g, "")
    const parts = pkg.replace(/(.)@[^/]*$/, "$1").split(/[\\/]/)
    const last = clean(parts.at(-1))
    server = { name: /^(mcp|server)?$/.test(last) && parts.length > 1 ? clean(parts.at(-2)) : last, command, args }
  }
  const base = name.trim() || server.name.replace(/[^\w-]+/g, "-") || "server"
  let unique = base
  for (let number = 2; !name.trim() && taken.includes(unique); number++) unique = `${base}-${number}`
  return { ...server, name: unique }
}

// The search box also takes a pasted server: a URL that isn't a GitHub source, or a command run by a package manager.
export function pastedServer(input: string, taken: string[]): MCPServerInput | undefined {
  const text = input.trim()
  return (/^https?:\/\//i.test(text) ? !sourceSpec(text) : runner.test(text)) ? serverFrom(text, "", taken) : undefined
}

// One thing this project has, or could have: installed here, pinned elsewhere in the mesh (Library), or in a catalog.
type Entry = {
  key: string
  kind: "skill" | "plugin" | "mcp"
  name: string
  description?: string
  from?: string
  // Other projects in the mesh that already use it.
  elsewhere: LibraryProject[]
  pkg?: ProjectPackage
  server?: ProjectMCP
  // Loaded because a plugin depends on it.
  included?: boolean
  spec?: string
  add?: MCPServerInput
  // Typed or pasted rather than found: installing clears the search.
  pasted?: boolean
}

const kindLabel = { skill: "Skill", plugin: "Plugin", mcp: "MCP server" }

// A project's page in the main pane, as VS Code's Extensions view: one search over what is installed here, what the
// mesh already pins (Library) and the public catalogs, with the same row for each. Installed rows carry their
// controls (Remove, the server switch and its login); the rest carry Install. Nothing to save; Remove offers Undo.
export function ProjectPage({ machine, directory, install }: { machine: string; directory: string; install: boolean }) {
  const queryClient = useQueryClient()
  const isPhone = useIsPhone()
  const origin = useRouterState({ select: (state) => state.location.state.sectionOrigin })
  const machineName = useMachines().find((item) => item.id === machine)?.name ?? machine
  const projects = useQuery(projectsQuery(machine))
  const summary = projects.data?.find((item) => item.directory === directory)
  const details = useQuery({ ...selectedProjectQuery(machine, directory), enabled: summary?.details_deferred === true })
  const active = summary?.details_deferred ? details : projects
  const project = summary?.details_deferred ? details.data : summary
  const { items: library } = useLibrary()
  const [text, setText] = useState("")
  const needle = text.trim().toLowerCase()
  const catalog = useQuery(catalogQuery(useSettled(text.trim())))
  const connections = useQuery(connectionsQuery(false))
  const credentials = connections.data?.credentials ?? []
  const signIn = useProjectSignIn(() => connections.refetch({ throwOnError: true }))
  const [busy, setBusy] = useState<Record<string, boolean>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  // A switch moves when clicked, before the project read catches up.
  const [toggled, setToggled] = useState<Record<string, boolean>>({})
  const [editing, setEditing] = useState<string>()
  const [confirm, setConfirm] = useState<ProjectMCP>()
  const [more, setMore] = useState(false)

  const refresh = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: projectsQuery(machine).queryKey }),
    queryClient.invalidateQueries({ queryKey: selectedProjectQuery(machine, directory).queryKey }),
    queryClient.invalidateQueries({ queryKey: packagesQuery(machine).queryKey }),
    queryClient.invalidateQueries({ queryKey: sessionsQuery(false).queryKey }),
  ])
  const failure = (error: unknown) => routeMissing(error) ? `Update agentpack on ${machineName} to change this project.` : errorText(error)
  const settle = (key: string, error?: string) => {
    setBusy((current) => without(current, key))
    setToggled((current) => without(current, key))
    if (error) setErrors((current) => ({ ...current, [key]: error }))
  }

  // `undo` reverses the change from the activity viewer, as Close does.
  function apply(key: string, patch: Patch, outcome?: { message: string; undo: Patch }, afterSignIn = false, done?: () => void) {
    setBusy((current) => ({ ...current, [key]: true }))
    setErrors((current) => without(current, key))
    void putSettings(machine, directory, patch, afterSignIn).then(
      async () => {
        await refresh()
        settle(key)
        done?.()
        if (outcome) hud.success(outcome.message, () => void putSettings(machine, directory, outcome.undo).then(refresh, (error: unknown) => hud.error(failure(error))))
      },
      (error: unknown) => settle(key, failure(error)),
    )
  }

  const name = project?.name || basename(directory)
  const packages = project?.packages ?? []
  const servers = project?.mcp ?? []
  const dependencies = packages.filter((pkg) => !pkg.direct)

  // Catalog entries describe what is installed too, matched by module or by server name and URL.
  const catalogItems = catalog.data?.items ?? []
  const catalogKey = (item: CatalogItem) => item.spec ? `pkg:${moduleOf(item.spec)}` : `mcp:${(item.server?.name ?? item.name).toLowerCase()}`
  const described = new Map(catalogItems.map((item) => [catalogKey(item), item]))
  const libraryByModule = new Map<string, PackageItem>()
  const libraryServers = new Map<string, LibraryProject[]>()
  for (const item of library) {
    if (item.type === "skill" || item.type === "plugin") for (const module of item.sources) libraryByModule.set(module.toLowerCase(), item)
    if (item.type === "mcp") libraryServers.set(item.name.toLowerCase(), item.projects)
  }
  const others = (list: LibraryProject[] = []) => list.filter((place) => !(place.machine === machineName && place.directory === directory))

  const entries = new Map<string, Entry>()
  for (const pkg of packages) {
    const key = `pkg:${pkg.module.toLowerCase()}`
    const info = described.get(key)
    entries.set(key, {
      key, kind: pkg.kind === "plugin" ? "plugin" : "skill", name: pkg.name || basename(pkg.module), description: info?.description,
      from: info?.publisher ?? shortSource(pkg.module), elsewhere: others(libraryByModule.get(pkg.module.toLowerCase())?.projects),
      ...(pkg.direct ? { pkg } : { included: true }),
    })
  }
  for (const server of servers) {
    const key = `mcp:${server.name.toLowerCase()}`
    const info = described.get(key) ?? catalogItems.find((item) => item.server?.url && item.server.url === server.url)
    const detail = server.source === "plugin" && server.module ? `from ${basename(server.module)}` : server.host || server.launcher
    entries.set(key, { key, kind: "mcp", name: server.name, description: info?.description, from: detail, elsewhere: others(libraryServers.get(server.name.toLowerCase())), server })
  }
  for (const item of library) {
    if (item.type !== "skill" && item.type !== "plugin") continue
    const key = `pkg:${item.module.toLowerCase()}`
    if (entries.has(key)) continue
    const info = described.get(key)
    entries.set(key, { key, kind: item.type, name: item.name, description: info?.description, from: info?.publisher ?? shortSource(item.module), elsewhere: others(item.projects), spec: `${item.module}@${item.commit}` })
  }
  for (const item of catalogItems) {
    const key = catalogKey(item)
    if (entries.has(key) || (!item.spec && !item.server)) continue
    entries.set(key, {
      key, kind: item.kind, name: item.name, description: item.description, from: item.publisher ?? item.source,
      elsewhere: item.kind === "mcp" ? others(libraryServers.get(item.name.toLowerCase())) : [],
      ...(item.spec ? { spec: item.spec } : { add: item.server }),
    })
  }

  const taken = servers.map((server) => server.name)
  const all = [...entries.values()]
  const available = (entry: Entry) => !entry.pkg && !entry.server && !entry.included
  // Pinned in the mesh first (already downloaded and verified), then the catalogs.
  const rank = (entry: Entry) => entry.pkg || entry.server || entry.included ? 0 : entry.elsewhere.length > 0 ? 1 : 2
  // Catalog servers (`add`) were already matched by the registry, which searches more than it returns.
  const results = needle ? all.filter((entry) => entry.add || `${entry.name} ${entry.description ?? ""} ${entry.from ?? ""}`.toLowerCase().includes(needle))
    .sort((a, b) => rank(a) - rank(b)) : []
  const spec = sourceSpec(text)
  if (spec && !entries.has(`pkg:${moduleOf(spec)}`)) results.unshift({ key: `source:${spec}`, kind: "plugin", name: shortSource(spec), description: "Install from this GitHub source", from: "GitHub", elsewhere: [], spec, pasted: true })
  const pasted = pastedServer(text, taken)
  if (pasted) results.unshift({ key: `add:${pasted.name}`, kind: "mcp", name: pasted.name, description: pasted.url ?? [pasted.command, ...(pasted.args ?? [])].join(" "), from: pasted.url ? new URL(pasted.url).host : "Command", elsewhere: [], add: pasted, pasted: true })
  const suggestions = all.filter(available).sort((a, b) => rank(a) - rank(b))
  // At rest Discover shows a few of each kind, so servers aren't buried under the plugin list.
  const firstSuggestions = [...suggestions.filter((entry) => entry.kind !== "mcp").slice(0, 5), ...suggestions.filter((entry) => entry.kind === "mcp").slice(0, 3)]

  // A found entry turns into its installed row in place; a pasted one clears the search, since its new row is
  // named differently from what was typed. A failure stays on the row with the text kept.
  function installEntry(entry: Entry) {
    const { key } = entry
    const done = () => { if (entry.pasted) setText("") }
    // The install routes, not a settings edit: they resolve and record the pin (npm version, commit) in pack.lock.
    const install = entry.add ? installMCP(machine, directory, entry.add, false) : entry.spec ? installPackage(machine, directory, entry.spec, false) : undefined
    if (install) {
      setBusy((current) => ({ ...current, [key]: true }))
      setErrors((current) => without(current, key))
      void install.then(async () => {
        await refresh()
        settle(key)
        done()
      }, (error: unknown) => settle(key, `Couldn't install ${entry.name}. ${failure(error)}`))
    }
  }

  function removeServer(server: ProjectMCP) {
    // A command server's arguments are not in the project read, so it cannot be put back: that one asks first.
    if (!server.url) return setConfirm(server)
    setEditing(undefined)
    apply(`mcp:${server.name.toLowerCase()}`, { remove_mcp: [server.name] }, { message: `Removed ${server.name} from ${name}`, undo: { servers: [{ name: server.name, url: server.url, type: server.transport }] } })
  }

  function setLogin(server: ProjectMCP, url: string, id: string, afterSignIn = false) {
    const patch: Patch = server.source === "plugin" && server.module
      ? { plugin_logins: { [server.name]: { module: server.module, url, credential: id } } }
      : { logins: { [url]: id } }
    apply(`mcp:${server.name.toLowerCase()}`, patch, undefined, afterSignIn)
  }

  function connect(server: ProjectMCP, url: string) {
    const labels = new Set(loginsFor(url, credentials).map((login) => login.label.toLowerCase()))
    let label = server.name
    for (let number = 2; labels.has(label.toLowerCase()); number++) label = `${server.name} ${number}`
    void signIn.connect(server.name, url, label, (id) => setLogin(server, url, id, true))
  }

  function row(entry: Entry) {
    const { key, pkg, server } = entry
    const elsewhere = entry.elsewhere.length > 0 ? (entry.pkg || entry.server ? `also in ${plural(entry.elsewhere.length, "project")}` : `in ${plural(entry.elsewhere.length, "project")}`) : undefined
    // The MCP servers list says what its rows are; mixed results and suggestions say it per row.
    const meta = [server && !needle ? undefined : kindLabel[entry.kind], entry.from, elsewhere].filter(Boolean).join(" · ")
    const tooltip = entry.elsewhere.map((place) => `${place.name} on ${place.machine}`).join("\n") || undefined
    const common = { title: entry.name, meta, tooltip, description: entry.description, busy: busy[key], error: errors[key] }
    if (pkg) {
      return (
        <Row key={key} {...common} action={<Button type="button" variant="ghost" size="sm" aria-label={`Remove ${entry.name}`}
          onClick={() => apply(key, { remove: [pkg.module] }, { message: `Removed ${entry.name} from ${name}`, undo: { add: [`${pkg.module}@${pkg.commit}`] } })}>Remove</Button>}>
          {/* Among search results, say which ones are already here; the Packages list needs no such word. */}
          {needle && <span className="text-(length:--fs-sm) text-(--fg2)">Installed</span>}
        </Row>
      )
    }
    if (server) {
      const on = toggled[key] ?? !server.disabled
      const { url } = server
      if (editing === server.name && url) {
        return <ServerForm key={key} server={server} busy={busy[key]} error={errors[key]} onCancel={() => setEditing(undefined)} onRemove={() => removeServer(server)}
          onSave={(next) => {
            setEditing(undefined)
            apply(key, { servers: [{ ...next, type: server.transport }] })
          }} />
      }
      const manifest = server.source === "manifest"
      const action = !manifest ? undefined
        : url ? <Button type="button" variant="ghost" size="sm" aria-label={`Edit ${server.name}`} onClick={() => setEditing(server.name)}>Edit…</Button>
        : <Button type="button" variant="ghost" size="sm" aria-label={`Remove ${server.name}`} onClick={() => removeServer(server)}>Remove</Button>
      return (
        <Row key={key} {...common} dim={!on} action={action}>
          {url && on && <ServerLogin server={server} url={url} credentials={credentials} canConnect={connections.isSuccess}
            pending={signIn.pending?.name === server.name} onChoose={(id) => setLogin(server, url, id)} onConnect={() => connect(server, url)} />}
          <Switch checked={on} aria-label={`Use ${server.name}`} onCheckedChange={(checked) => {
            setToggled((current) => ({ ...current, [key]: checked }))
            apply(key, { mcp_enabled: { [server.name]: checked } })
          }} />
        </Row>
      )
    }
    if (entry.included) return <Row key={key} {...common}><span className="text-(length:--fs-sm) text-(--fg2)">Included by a plugin</span></Row>
    return <Row key={key} {...common}><Button type="button" size="sm" disabled={busy[key]} aria-label={`Install ${entry.name}`} onClick={() => installEntry(entry)}>{busy[key] ? "Installing…" : "Install"}</Button></Row>
  }

  const ready = project?.available && !project.error
  const unavailable = active.isError ? errorText(active.error)
    : !active.isPending && !project ? "Project folder unavailable."
    : project && !project.available ? project.unavailable_reason || "Project folder unavailable."
    : project?.error
  const catalogProblem = catalog.isError
    ? routeMissing(catalog.error) || (catalog.error instanceof ApiError && catalog.error.status === 404) ? "This gateway needs an update to search the catalog." : "Couldn't reach the catalog, so only this project and your Library were searched."
    : catalog.data?.sources.filter((source) => source.error).map((source) => `Couldn't reach ${source.name}. ${source.error}`).join(" ")
  const catalogNote = (catalogProblem || (catalog.isFetching && needle)) && (
    <p role="status" className="flex items-center gap-(--s3) px-(--s2) text-(length:--fs-sm) text-(--fg2)">
      {catalogProblem ? <>{catalogProblem}<Button type="button" variant="ghost" size="sm" onClick={() => void catalog.refetch()}>Retry</Button></> : <><Spinner /> Searching the catalog…</>}
    </p>
  )

  return (
    <div className="h-full min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex max-w-[52rem] flex-col gap-(--s8) px-(--s8) py-(--s8) max-[760px]:px-(--s5) max-[760px]:py-(--s6)">
        <header className="flex items-start gap-(--s3)">
          {!isPhone && (
            <Link to="/sessions" href={origin} aria-label="Back" title="Back" className="-ms-(--s3) mt-[2px] grid size-(--ctl-h) shrink-0 place-items-center rounded-(--r-ctl) hover:bg-(--hover)">
              <ChevronLeft aria-hidden className="size-4" />
            </Link>
          )}
          <div className="flex min-w-0 flex-1 flex-col gap-(--s1)">
            <h2 className="truncate text-(length:--fs-lg) font-semibold">{name}</h2>
            <p className="truncate text-(length:--fs-sm) text-(--fg2)" title={directory}>{`${machineName} · ${directory}`}</p>
          </div>
        </header>

        {active.isPending && <p role="status" className="flex items-center gap-(--s3) text-(--fg2)"><Spinner /> Reading {name} on {machineName}…</p>}
        {unavailable && <FieldError>{unavailable}</FieldError>}

        {ready && (
          <>
            <SearchField autoFocus={install} aria-label="Search skills, plugins and MCP servers" placeholder={isPhone ? "Search skills, plugins and servers" : "Search skills, plugins and MCP servers, or paste a GitHub source or server URL"}
              className="max-[760px]:h-11" value={text} onChange={(event) => setText(event.target.value)}
              onKeyDown={(event) => { if (event.key === "Escape" && text) { event.preventDefault(); setText("") } }} />

            {needle ? (
              <section aria-label="Results" className="flex flex-col gap-(--s3)">
                {results.length > 0 && <FormGroup title={plural(results.length, "result")}>{results.map(row)}</FormGroup>}
                {catalogNote}
                {results.length === 0 && !catalog.isFetching && !catalogProblem && (
                  <Empty title={`Nothing matches “${text.trim()}”`}>Paste a GitHub source, like owner/repo, or an MCP server's URL or command.</Empty>
                )}
              </section>
            ) : (
              <>
                <FormGroup title="Packages" footer={dependencies.length > 0 && `Plugins also load ${dependencies.map((pkg) => pkg.name || basename(pkg.module)).join(", ")}.`}>
                  {all.filter((entry) => entry.pkg).map(row)}
                  {!all.some((entry) => entry.pkg) && <p className="px-(--s5) py-(--s4) text-(length:--fs-sm) text-(--fg2)">No skills or plugins yet. Search above to add one.</p>}
                </FormGroup>
                <div className="flex flex-col gap-(--s3)">
                  <FormGroup title="MCP servers" footer="Paste a server's URL or command into the search to add your own.">
                    {all.filter((entry) => entry.server).map(row)}
                    {servers.length === 0 && <p className="px-(--s5) py-(--s4) text-(length:--fs-sm) text-(--fg2)">No MCP servers yet. Search above to add one.</p>}
                  </FormGroup>
                  <ProjectSignInStatus signIn={signIn} />
                </div>
                <section aria-label="Discover" className="flex flex-col gap-(--s3)">
                  <FormGroup title="Discover" action={suggestions.length > firstSuggestions.length && (
                    <button type="button" onClick={() => setMore(!more)} className="text-(length:--fs-sm) text-(--accent) hover:underline">{more ? "Show fewer" : `Show all ${suggestions.length}`}</button>
                  )}>
                    {(more ? suggestions : firstSuggestions).map(row)}
                    {suggestions.length === 0 && !catalog.isPending && <p className="px-(--s5) py-(--s4) text-(length:--fs-sm) text-(--fg2)">Everything in your Library and the catalog is already here.</p>}
                    {catalog.isPending && <p className="flex items-center gap-(--s3) px-(--s5) py-(--s4) text-(length:--fs-sm) text-(--fg2)"><Spinner /> Loading the catalog…</p>}
                  </FormGroup>
                  {catalogNote}
                </section>
              </>
            )}
            <p className="text-(length:--fs-sm) text-(--fg2)">Changes reach agents in {name} when they're idle.</p>
          </>
        )}

        <ConfirmAlert open={Boolean(confirm)} onOpenChange={(open) => { if (!open) setConfirm(undefined) }}>
          {confirm && (
            <ConfirmAlertContent title={`Remove “${confirm.name}”?`} destructiveLabel="Remove" destructive={false}
              description={`Agents in ${name} lose its tools. Its command can't be restored from here.`}
              onConfirm={() => {
                apply(`mcp:${confirm.name.toLowerCase()}`, { remove_mcp: [confirm.name] })
                setConfirm(undefined)
              }} />
          )}
        </ConfirmAlert>
      </div>
    </div>
  )
}

// Two lines: the name with what it is and where it comes from, then its description. `action` is the rarer
// control and shows on hover or focus (on touch, once the row is tapped); `children` always show.
function Row({ title, meta, tooltip, description, dim, busy, error, action, children }: {
  title: string
  meta?: string
  tooltip?: string
  description?: string
  dim?: boolean
  busy?: boolean
  error?: string
  action?: ReactNode
  children?: ReactNode
}) {
  return (
    // On touch the action waits for a tap on its row, so a stray tap on an unseen Remove does nothing.
    <div tabIndex={action ? -1 : undefined} className="group/row flex min-h-(--row-h-2) min-w-0 flex-col justify-center gap-(--s1) border-t-[0.5px] border-(--sep-soft) px-(--s5) py-(--s3) outline-none first:border-t-0 pointer-coarse:focus-within:bg-(--hover)">
      <div className="flex min-w-0 items-center gap-(--s3)">
        <span className={cn("flex min-w-0 flex-1 flex-col", dim && "opacity-50")}>
          <span className="flex min-w-0 items-baseline gap-(--s3)" title={tooltip}>
            <span className="shrink-0 truncate font-medium">{title}</span>
            {meta && <span className="min-w-0 truncate text-(length:--fs-sm) text-(--fg2)">{meta}</span>}
          </span>
          {description && <span className="truncate text-(length:--fs-sm) text-(--fg2) max-[760px]:line-clamp-2 max-[760px]:whitespace-normal">{description}</span>}
        </span>
        {busy && <Spinner />}
        {action && !busy && (
          <span className="opacity-0 transition-opacity duration-(--dur-1) group-hover/row:opacity-100 group-focus-within/row:opacity-100 pointer-coarse:invisible pointer-coarse:group-focus-within/row:visible">{action}</span>
        )}
        {children}
      </div>
      {error && <FieldError>{error}</FieldError>}
    </div>
  )
}

// Which login a remote server uses, as a pop-up of logins ending in "Add account…", as a printer pop-up ends
// in "Add Printer…". No login yet, or one that expired, is a Sign in button.
function ServerLogin({ server, url, credentials, canConnect, pending, onChoose, onConnect }: {
  server: ProjectMCP
  url: string
  credentials: Connection[]
  canConnect: boolean
  pending: boolean
  onChoose: (id: string) => void
  onConnect: () => void
}) {
  const logins = loginsFor(url, credentials)
  const plugin = server.source === "plugin"
  if (plugin && !server.module) return <span className="text-(length:--fs-sm) text-(--fg2)" title="Update this machine to pick a login for plugin servers.">Needs update</span>
  if (pending) return <span className="flex items-center gap-(--s2) text-(length:--fs-sm) text-(--fg2)"><Spinner /> Signing in…</span>
  const current = logins.find((login) => login.id === server.login) ?? (plugin ? undefined : logins[0])
  if (logins.length === 0 || current?.status === "needs_login") {
    return canConnect ? <Button type="button" size="sm" onClick={onConnect} title="Opens in a new tab">Sign in…</Button> : null
  }
  return (
    <Menu>
      <MenuTrigger render={<Button type="button" variant="ghost" size="sm" aria-label={`${server.name} login: ${current?.label ?? "none chosen"}`} className="text-(--fg2)" />}>
        <span className="max-w-32 truncate">{current?.label ?? "Choose login"}</span>
        <ChevronDown aria-hidden className="size-3" />
      </MenuTrigger>
      <MenuContent align="end">
        {logins.map((login) => (
          <MenuItem key={login.id} onClick={() => onChoose(login.id)} icon={<Check aria-hidden className={cn("size-3.5", login.id !== current?.id && "invisible")} />}>{login.label}</MenuItem>
        ))}
        <DropdownMenuSeparator />
        <MenuItem disabled={!canConnect} onClick={onConnect} icon={<span aria-hidden className="size-3.5" />}>Add account…</MenuItem>
      </MenuContent>
    </Menu>
  )
}

// Edits a project server's URL in place of its row; Remove sits beside it.
function ServerForm({ server, busy, error, onCancel, onSave, onRemove }: {
  server: ProjectMCP
  busy?: boolean
  error?: string
  onCancel: () => void
  onSave: (server: MCPServerInput) => void
  onRemove: () => void
}) {
  const [source, setSource] = useState(server.url ?? "")
  const [problem, setProblem] = useState("")

  function submit(event: FormEvent) {
    event.preventDefault()
    const next = serverFrom(source, server.name, [])
    const message = next?.url ? "" : "Enter an http or https URL."
    setProblem(message)
    if (next && !message) onSave(next)
  }

  return (
    <form onSubmit={submit} onKeyDown={(event) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); onCancel() } }}
      aria-label={`Edit ${server.name}`} className="flex flex-col gap-(--s3) border-t-[0.5px] border-(--sep-soft) bg-(--raised) px-(--s5) py-(--s4) first:border-t-0">
      <p className="font-medium">{server.name}</p>
      <Input autoFocus disabled={busy} aria-label="URL" value={source} onChange={(event) => setSource(event.target.value)} placeholder="https://…" />
      {(problem || error) && <FieldError>{problem || error}</FieldError>}
      <div className="flex items-center gap-(--s3)">
        <Button type="button" size="sm" variant="ghost" className="text-(--danger-word)" onClick={onRemove}>Remove server</Button>
        <Button type="button" size="sm" className="ms-auto" onClick={onCancel}>Cancel</Button>
        <Button type="submit" size="sm" variant="primary" disabled={busy}>Save</Button>
      </div>
    </form>
  )
}
