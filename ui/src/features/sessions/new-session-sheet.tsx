import { useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react"
import { loadStartChoices, markNewChat, saveStartChoices } from "./choices"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { FieldError, PopupSelect } from "@/components/kit/forms"
import { Kbd } from "@/components/kit/feedback"
import { GroupHeader, SearchField, SegmentedControl, SegmentedControlItem, SourceList, SourceRow } from "@/components/kit/list"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { api, ApiError } from "@/lib/api"
import type { AgentSession, ProjectRow, SessionInfo } from "@/lib/api-types"
import { basename, harnessName, plural, shortAge } from "@/lib/format"
import { cn } from "@/lib/utils"
import { sessionsQuery, projectsQuery } from "./queries"
import { agentIds, canStart, defaultAgent, mergeProjects, modeChoices, modeEffect, placeKey, placeLabel, placeProblem, startSessionBody, type MergedProject, type SessionView } from "./rules"

// Harness ids the gateway starts.
const DAY = 24 * 60 * 60 * 1000
const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

function chatUnavailableReason(error: unknown, machine: string, harness: string): string | undefined {
  if (error instanceof ApiError && error.status === 404) {
    return `Update Agentpack on ${machine} before starting Chat. Terminal remains available.`
  }
  const message = error instanceof Error ? error.message : ""
  if (message.includes("Node.js is required for ACP chat")) {
    return `Install Node.js on ${machine} to use Chat, or choose Terminal.`
  }
  if (harness === "grok" && /authentication required|login required/i.test(message)) {
    return `Sign in to ${harnessName(harness)} on ${machine} before using Chat, or choose Terminal.`
  }
  return undefined
}

// Opened from ?new=1, optionally with ?machine= and ?folder= from a sidebar folder.
// The form mounts only while open, so each open starts from the defaults.
export function NewSessionSheet({
  open,
  initialMachine,
  initialDirectory,
  onOpenChange,
}: {
  open: boolean
  initialMachine?: string
  initialDirectory?: string
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {open && (
        <SheetContent className="h-[min(640px,calc(100dvh-var(--bar-h)-var(--s6)))] w-[min(900px,calc(100vw-var(--s6)*2))] max-w-none sm:max-w-none max-[760px]:w-full grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 max-[760px]:h-[92dvh]">
          <NewSessionForm initialMachine={initialMachine} initialDirectory={initialDirectory} />
        </SheetContent>
      )}
    </Sheet>
  )
}

function NewSessionForm({ initialMachine, initialDirectory }: { initialMachine?: string; initialDirectory?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const machines = useMachines()
  const projectQueries = useMachineQueries(projectsQuery)
  const online = machines.filter((machine) => machine.online)
  // Nulls mean "use the default": the requested folder, else the newest project on its newest machine.
  const [projectKey, setProjectKey] = useState<string | null>(null)
  const [chosenPlace, setChosenPlace] = useState<string | null>(null)
  const [harness, setHarness] = useState<string | null>(null)
  const [mode, setMode] = useState<string | null>(null)
  const [chosenView, setView] = useState<SessionView | null>(null)
  const [resumeId, setResumeId] = useState("")
  const [query, setQuery] = useState("")

  // A folder opened from the sidebar is offered even before (or without) the machine listing it.
  const rows: Record<string, ProjectRow[] | undefined> = {}
  for (const machine of machines) rows[machine.id] = projectQueries.byMachine[machine.id]?.data
  const requested = initialMachine && initialDirectory ? placeKey(initialMachine, initialDirectory) : undefined
  if (initialMachine && initialDirectory && !(rows[initialMachine] ?? []).some((row) => placeKey(initialMachine, row.directory) === requested)) {
    rows[initialMachine] = [...(rows[initialMachine] ?? []), stubRow(initialDirectory)]
  }
  const projects = mergeProjects(machines, rows)
  const requestedProject = requested ? projects.find((item) => item.places.some((place) => place.key === requested)) : undefined
  const project = projects.find((item) => item.key === projectKey) ?? requestedProject ?? projects.find((item) => item.places.some(canStart)) ?? projects[0]
  const place =
    project?.places.find((item) => item.key === chosenPlace) ??
    project?.places.find((item) => item.key === requested) ??
    project?.places.find(canStart) ??
    project?.places[0]
  const row = place?.row
  const placeQuery = place ? projectQueries.byMachine[place.machine] : undefined
  const contentsLoading = Boolean(placeQuery?.pending)
  const newest = row?.launches[0]
  // What this browser last started here wins over the folder's newest launch.
  const remembered = row ? loadStartChoices(row.directory) : {}
  const harnessId = harness ?? defaultAgent(remembered, row?.launches ?? []).harness
  const modes = row ? modeChoices(row) : ["default"]
  const modeValue = mode && modes.includes(mode) ? mode : remembered.mode && modes.includes(remembered.mode) && (!harness || harness === remembered.harness) ? remembered.mode : newest?.mode || "default"
  const view = chosenView ?? remembered.view ?? "chat"
  const machineName = place?.machineName ?? "this machine"
  const startable = place ? canStart(place) : false

  const pastSessions = useQuery({
    queryKey: ["agent-sessions", place?.machine, row?.directory, harnessId, modeValue],
    queryFn: async () => {
      const body = await api<{ sessions: AgentSession[] }>("/api/agent-sessions", {
        machine: place!.machine,
        query: { directory: row!.directory, harness: harnessId, mode: modeValue },
      })
      return body.sessions ?? []
    },
    enabled: view === "chat" && startable,
    retry: false,
  })

  const start = useMutation({
    mutationFn: (input: { machine: string; directory: string; harness: string; mode: string; view: SessionView; agentSessionId?: string }) =>
      api<SessionInfo>("/api/sessions", { machine: input.machine, method: "POST", body: startSessionBody(input) }),
    onSuccess: (info, input) => {
      saveStartChoices(input.directory, { harness: input.harness, mode: input.mode, view: input.view })
      if (input.view === "chat" && !input.agentSessionId) markNewChat(info.id)
      void queryClient.invalidateQueries({ queryKey: sessionsQuery(input.machine, false).queryKey })
      void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: input.machine, sessionId: info.id } })
    },
  })

  function chooseProject(next: MergedProject) {
    setProjectKey(next.key)
    setChosenPlace(null)
    setHarness(null)
    setMode(null)
    setResumeId("")
  }

  // The agent stays as chosen; mode and conversation belong to the folder on that machine.
  function choosePlace(next: string) {
    setChosenPlace(next)
    setMode(null)
    setResumeId("")
  }

  function chooseView(value: unknown[]) {
    const next = value[0]
    if (next !== "chat" && next !== "terminal") return
    setView(next)
    if (next !== "chat") setResumeId("")
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (place && row && !blocked) {
      start.mutate({ machine: place.machine, directory: row.directory, harness: harnessId, mode: modeValue, view, agentSessionId: resumeId || undefined })
    }
  }

  function submitShortcut(event: KeyboardEvent<HTMLFormElement>) {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault()
      event.currentTarget.requestSubmit()
    }
  }

  // One reason at a time, most blocking first.
  const chatUnavailableHint = chatUnavailableReason(pastSessions.error, machineName, harnessId)
  const placeError = placeQuery?.error
  let hint: string | undefined
  if (online.length === 0) hint = "No machine is online."
  else if (place && !place.online) hint = `${machineName} is offline. Choose another machine for ${project?.name}.`
  else if (row && !row.available) hint = `${row.unavailable_reason || "This folder is missing"} on ${machineName}. Choose another machine.`
  else if (placeQuery?.needsUpdate) hint = `Update Agentpack on ${machineName} to see its projects.`
  else if (placeError) hint = `Couldn't load projects from ${machineName}: ${placeError.message}`
  else if (!projectQueries.firstLoadPending && projects.length === 0) hint = "Run an agent in a folder on a machine first, for example agentpack claude. It then appears here."
  else if (chatUnavailableHint) hint = chatUnavailableHint
  else if (view === "chat" && pastSessions.error) hint = `Couldn't load conversations: ${(pastSessions.error as Error).message}`
  const blocked = !startable || start.isPending || (view === "chat" && Boolean(chatUnavailableHint))
  const failedMachines = machines.filter((machine) => machine.online && projectQueries.byMachine[machine.id]?.error)

  const needle = query.trim().toLowerCase()
  const shown = projects.filter(
    (item) => !needle || [item.name, ...item.places.flatMap((entry) => [entry.row.directory, entry.machineName])].some((part) => part.toLowerCase().includes(needle)),
  )
  const now = Date.now()
  const recent = shown.filter((item) => now - item.lastUsed < DAY)
  const older = shown.filter((item) => now - item.lastUsed >= DAY)
  const projectRow = (item: MergedProject) => {
    const best = item.places.find(canStart)
    return (
      <SourceRow
        key={item.key}
        type="button"
        selected={item === project}
        title={item.places.map((entry) => `${entry.machineName}: ${entry.row.directory}`).join("\n")}
        onClick={() => chooseProject(item)}
        className={cn("h-auto flex-col items-stretch gap-0 py-(--s3) max-[760px]:h-auto", !best && "opacity-50")}
      >
        <span className="flex min-w-0 items-baseline gap-(--s3)">
          <span className="min-w-0 flex-1 truncate font-semibold">{item.name}</span>
          {item.lastUsed > 0 && <span className="shrink-0 text-(length:--fs-xs) opacity-70">{shortAge(new Date(item.lastUsed), now)}</span>}
        </span>
        <span className="truncate text-(length:--fs-xs) opacity-70">{[...new Set(item.places.map((entry) => entry.machineName))].join(" · ")}</span>
        <span className="truncate text-(length:--fs-xs) opacity-70">{best ? contentsSummary(best.row) : `Can't start: ${placeProblem(item.places[0]!)}`}</span>
      </SourceRow>
    )
  }

  return (
    <form onSubmit={submit} onKeyDown={submitShortcut} className="contents">
      <header className="flex min-w-0 items-center gap-(--s4) border-b-[0.5px] border-(--sep) px-(--s6) py-(--s5)">
        <div className="min-w-0 flex-1">
          <SheetTitle>New session</SheetTitle>
          <SheetDescription>Pick a project, then the machine to run it on.</SheetDescription>
        </div>
      </header>

      <div className="grid min-h-0 grid-cols-[280px_minmax(0,1fr)] max-[760px]:grid-cols-1 max-[760px]:grid-rows-[minmax(0,180px)_minmax(0,1fr)]">
        <aside className="flex min-h-0 flex-col gap-(--s3) border-r-[0.5px] border-(--sep) bg-(--sidebar) pt-(--s4) max-[760px]:border-r-0 max-[760px]:border-b-[0.5px]">
          <div className="px-(--s4)">
            <SearchField aria-label="Search projects" placeholder="Search projects or machines" value={query} onChange={(event) => setQuery(event.target.value)} />
          </div>
          <SourceList aria-label="Projects" className="flex-1 px-(--s3)">
            {projectQueries.firstLoadPending && projects.length === 0 ? (
              <p className="px-(--s3) text-(--fg2)">Loading projects…</p>
            ) : projects.length === 0 ? (
              <p className="px-(--s3) text-(--fg2)">No projects yet</p>
            ) : shown.length === 0 ? (
              <p className="px-(--s3) text-(--fg2)">No project matches “{query}”.</p>
            ) : (
              <>
                {recent.length > 0 && <GroupHeader>Today</GroupHeader>}
                {recent.map(projectRow)}
                {older.length > 0 && <GroupHeader>Earlier</GroupHeader>}
                {older.map(projectRow)}
              </>
            )}
          </SourceList>
          {failedMachines.length > 0 && (
            <p className="border-t-[0.5px] border-(--sep) px-(--s4) py-(--s3) text-(length:--fs-xs) text-(--fg2)">
              {failedMachines.map((machine) => machine.name).join(", ")}: projects unavailable
            </p>
          )}
        </aside>

        <div className="flex min-h-0 flex-col gap-(--s6) overflow-y-auto px-(--s6) py-(--s5)">
          {project && place && row ? (
            <>
              <div className="flex min-w-0 flex-col gap-(--s3)">
                <h2 className="truncate text-(length:--fs-lg) font-semibold">{project.name}</h2>
                <div className="flex min-w-0 items-center gap-(--s4) max-[760px]:flex-col max-[760px]:items-stretch">
                  <span className="shrink-0 text-(length:--fs-xs) font-semibold text-(--fg2)">Run on</span>
                  <PopupSelect
                    label="Machine"
                    value={place.key}
                    onValueChange={choosePlace}
                    items={project.places.map((entry) => ({ value: entry.key, label: placeLabel(entry, project.places), disabled: !canStart(entry) }))}
                  />
                  <span className="min-w-0 truncate font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2)" title={row.directory}>
                    {row.directory}
                  </span>
                </div>
              </div>

              <Choice label="Agent">
                <div role="group" aria-label="Agent" className="flex flex-wrap gap-(--s2)">
                  {agentIds.map((id) => {
                    const used = row.launches.find((launch) => launch.harness === id)
                    return (
                      <Button key={id} type="button" variant={id === harnessId ? "primary" : "default"} aria-pressed={id === harnessId} onClick={() => { setHarness(id); setResumeId("") }}>
                        {harnessName(id)}
                        {used && <span className="text-(length:--fs-xs) opacity-70">{shortAge(new Date(used.last_used), now)}</span>}
                      </Button>
                    )
                  })}
                </div>
              </Choice>

              <div className="grid grid-cols-2 gap-(--s6) max-[760px]:grid-cols-1">
                <Choice label="Mode" note="chooses what loads below">
                  <SegmentedControl value={[modeValue]} onValueChange={(value) => typeof value[0] === "string" && setMode(value[0])} aria-label="Mode" className="w-full">
                    {modes.map((name) => (
                      <SegmentedControlItem key={name} value={name} className="min-w-0 flex-1 truncate">
                        {name}
                      </SegmentedControlItem>
                    ))}
                  </SegmentedControl>
                </Choice>
                <Choice label="View">
                  <SegmentedControl value={[view]} onValueChange={chooseView} aria-label="Session view" className="w-full">
                    <SegmentedControlItem value="chat" className="flex-1" disabled={Boolean(chatUnavailableHint)}>
                      Chat
                    </SegmentedControlItem>
                    <SegmentedControlItem value="terminal" className="flex-1">
                      Terminal
                    </SegmentedControlItem>
                  </SegmentedControl>
                </Choice>
              </div>

              {contentsLoading ? (
                <p className="text-(--fg2)">Loading what this folder loads on {machineName}…</p>
              ) : (
                <LoadedContents project={row} mode={modeValue} />
              )}

              {view === "chat" && startable && (
                <Choice label="Conversation">
                  {pastSessions.isPending ? (
                    <span className="text-(--fg2)">Loading conversations…</span>
                  ) : (
                    <div role="radiogroup" aria-label="Conversation" className="flex max-h-48 flex-col overflow-y-auto">
                      {[{ id: "", title: "Start a new conversation" }, ...(pastSessions.data ?? [])].map((item) => (
                        <label key={item.id} className="flex h-(--row-h) min-w-0 items-center gap-(--s4) rounded-(--r-row) px-(--s3) hover:bg-(--hover)">
                          <input type="radio" name="conversation" value={item.id} checked={resumeId === item.id} onChange={() => setResumeId(item.id)} className="accent-(--accent)" />
                          <span className="min-w-0 flex-1 truncate">{item.id ? item.title?.trim() || item.id : item.title}</span>
                        </label>
                      ))}
                    </div>
                  )}
                </Choice>
              )}
            </>
          ) : (
            <p className="text-(--fg2)">{hint ?? "Choose a project."}</p>
          )}
        </div>
      </div>

      <footer className="flex min-w-0 flex-col gap-(--s3) border-t-[0.5px] border-(--sep) bg-(--sidebar) px-(--s6) py-(--s4)">
        {place && hint && <p className="text-(length:--fs-sm) text-(--fg2)">{hint}</p>}
        {start.error && <FieldError>{start.error.message}</FieldError>}
        <div className="flex min-w-0 items-center gap-(--s4) max-[760px]:flex-col max-[760px]:items-stretch">
          <p className="min-w-0 flex-1 truncate text-(length:--fs-sm) text-(--fg2)" aria-live="polite">
            {project && place && (
              <>
                <b className="font-semibold text-(--fg)">{harnessName(harnessId)}</b> · {modeValue} · {view === "chat" ? "Chat" : "Terminal"} in{" "}
                <b className="font-semibold text-(--fg)">{project.name}</b> on <b className="font-semibold text-(--fg)">{machineName}</b>
                {resumeId && " · resumes a conversation"}
              </>
            )}
          </p>
          <SheetActions>
            <Button type="submit" variant="primary" disabled={blocked}>
              {start.isPending ? "Starting session…" : "Start session"}
              <Kbd aria-hidden className="text-current opacity-70">{isMac ? "⌘↵" : "Ctrl+↵"}</Kbd>
            </Button>
          </SheetActions>
        </div>
      </footer>
    </form>
  )
}

// A folder the machine has not listed yet: startable, nothing known about its contents.
function stubRow(directory: string): ProjectRow {
  return { directory, name: basename(directory), folder_id: "", available: true, unavailable_reason: "", last_used: "", launches: [], packages: [], mcp: [], modes: [], error: "" }
}

function contentsSummary(project: ProjectRow): string {
  if (project.error) return "Couldn't read pack.lock"
  if (project.packages.length + project.mcp.length === 0) return "No packages"
  return `${plural(project.packages.length, "package")} · ${project.mcp.length} MCP`
}

function Choice({ label, note, children }: { label: string; note?: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-(--s3)">
      <span className="text-(length:--fs-xs) font-semibold text-(--fg2)">
        {label}
        {note && <span className="font-normal text-(--fg3)"> · {note}</span>}
      </span>
      {children}
    </div>
  )
}

// Everything the session loads from pack.lock, with what the chosen mode turns off.
function LoadedContents({ project, mode }: { project: ProjectRow; mode: string }) {
  const effect = modeEffect(project, mode)
  const off = project.packages.filter((pkg) => effect.offPackages.has(pkg.module)).length + project.mcp.filter((server) => effect.offMCP.has(server.name)).length
  const active = project.packages.length + project.mcp.length - off
  return (
    <section aria-label="Loaded in this session" className="flex min-w-0 flex-col gap-(--s3)">
      <span className="text-(length:--fs-xs) font-semibold text-(--fg2)">
        Loaded in this session
        <span className="font-normal text-(--fg3)">
          {" "}
          · {active} active{off > 0 && `, ${off} turned off by mode “${mode}”`}
        </span>
      </span>
      {project.error ? (
        <p className="text-(length:--fs-sm) text-(--danger)">Couldn't read this project's pack.lock: {project.error}</p>
      ) : (
        <div className="overflow-hidden rounded-(--r-pop) border-[0.5px] border-(--sep)">
          <ContentsGroup title="Skills & plugins" count={project.packages.length} empty="No packages. Only the agent's own skills load.">
            {project.packages.map((pkg) => (
              <ContentsRow
                key={pkg.module}
                name={pkg.name || pkg.module.split("/").pop() || pkg.module}
                detail={pkg.module}
                tag={pkg.kind}
                off={effect.offPackages.has(pkg.module)}
                status={effect.partialPackages.has(pkg.module) ? `partly off in ${mode}` : pkg.direct ? "direct" : "via dependency"}
                mode={mode}
              />
            ))}
          </ContentsGroup>
          <ContentsGroup title="MCP servers" count={project.mcp.length} empty="No MCP servers recorded in pack.lock.">
            {project.mcp.map((server) => (
              <ContentsRow
                key={server.name}
                name={server.name}
                detail={[server.package && (server.version ? `${server.package}@${server.version}` : server.package), server.host, server.source && `from ${server.source}`].filter(Boolean).join(" · ")}
                tag={server.launcher}
                off={effect.offMCP.has(server.name)}
                status={server.status}
                tone={server.status === "pinned" ? "ok" : server.status === "unpinned" ? "warn" : undefined}
                mode={mode}
              />
            ))}
          </ContentsGroup>
        </div>
      )}
    </section>
  )
}

function ContentsGroup({ title, count, empty, children }: { title: string; count: number; empty: string; children: ReactNode }) {
  return (
    <div className="border-b-[0.5px] border-(--sep) last:border-b-0">
      <GroupHeader className="bg-(--sidebar) px-(--s5)">
        {title}
        <span className="font-normal text-(--fg3)">{count}</span>
      </GroupHeader>
      {count === 0 ? <p className="px-(--s5) py-(--s3) text-(length:--fs-sm) text-(--fg2)">{empty}</p> : <ul>{children}</ul>}
    </div>
  )
}

function ContentsRow({ name, detail, tag, status, tone, off, mode }: { name: string; detail: string; tag: string; status: string; tone?: "ok" | "warn"; off: boolean; mode: string }) {
  return (
    <li data-off={off || undefined} className="flex min-w-0 items-center gap-(--s4) border-t-[0.5px] border-(--sep-soft) px-(--s5) py-(--s3) first:border-t-0 data-off:opacity-50">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-(--s3)">
          <span className={cn("truncate font-medium", off && "line-through")}>{name}</span>
          {tag && <Tag>{tag}</Tag>}
        </div>
        {detail && <div className="truncate font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2)">{detail}</div>}
      </div>
      <Tag className={cn(!off && tone === "ok" && "text-(--ok-word)", !off && tone === "warn" && "text-(--warn-word)")}>{off ? `off in ${mode}` : status}</Tag>
    </li>
  )
}

function Tag({ className, children }: { className?: string; children: ReactNode }) {
  return <span className={cn("shrink-0 rounded-(--r-row) border-[0.5px] border-(--sep) px-(--s2) text-(length:--fs-xs) whitespace-nowrap text-(--fg2)", className)}>{children}</span>
}
