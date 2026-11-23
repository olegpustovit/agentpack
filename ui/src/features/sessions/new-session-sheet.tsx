import { Fragment, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react"
import { loadProjectFilters, loadStartChoices, saveProjectFilters } from "./choices"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { FieldError, PopupSelect } from "@/components/kit/forms"
import { StatusGlyph } from "@/components/kit/status-dot"
import { Kbd } from "@/components/kit/feedback"
import { GroupHeader, SegmentedControl, SegmentedControlItem, SourceList, SourceRow } from "@/components/kit/list"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { useMachineQueries, useMachines, useReplicatedQueries } from "@/features/machines/queries"
import { agentsQuery } from "@/features/agents/queries"
import { api, ApiError, errorText, LOCAL, routeMissing } from "@/lib/api"
import type { AgentSession, ProjectRow } from "@/lib/api-types"
import { harnessName, plural, relativeTime, shortAge } from "@/lib/format"
import { cn } from "@/lib/utils"
import { agentIds, canStart, defaultAgent, mergeProjects, modeChoices, modeEffect, placeKey, placeLabel, projectGroups, projectLocation, stubRow, type MergedProject, type SessionView } from "./rules"
import { useStartSession } from "./start"
import { useEventsConnected } from "@/features/events/use-events"
import { LoginGlyph } from "@/features/library/mcp-inspector"
import { connectionsQuery, loginsFor, signIn, type Connection } from "@/features/settings/connections-model"
import { hud } from "@/lib/hud"
import { branchSlug, projectDirectory, worktreeFolderInProject, worktreesQuery } from "./git"
import { foundQuery, projectsQuery, sessionsQuery } from "./queries"

import { FolderBrowser, useFolderBrowser } from "./folder-browser"
const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

function chatUnavailableReason(error: unknown, machine: string): string | undefined {
  if (routeMissing(error)) {
    return `Update Agentpack on ${machine} before starting Chat. Terminal remains available.`
  }
  const message = error instanceof Error ? error.message : ""
  if (message.includes("Node.js is required for ACP chat")) {
    return `Install Node.js on ${machine} to use Chat, or choose Terminal.`
  }
  return undefined
}

// An agent that isn't signed in can't list its past chats; the sign-in line below covers it.
const signInError = (error: unknown) => error instanceof Error && /authentication required|login required|not signed in/i.test(error.message)

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
  const machines = useMachines()
  const projectQueries = useMachineQueries(projectsQuery)
  const foundQueries = useMachineQueries(foundQuery)
  const online = machines.filter((machine) => machine.online)
  // Default to the requested folder or the newest startable project.
  const [filters, setFilters] = useState(() => {
    const saved = loadProjectFilters()
    return { ...saved, machine: initialMachine ?? saved.machine }
  })
  const [picked, setPicked] = useState<{ machine: string; row: ProjectRow; pathQuery?: string } | undefined>(() =>
    initialMachine && initialDirectory ? { machine: initialMachine, row: stubRow(initialDirectory) } : undefined,
  )
  const [selectedProject, setSelectedProject] = useState<string | null>(null)
  const [chosenPlace, setChosenPlace] = useState<string | null>(null)
  const [harness, setHarness] = useState<string | null>(null)
  const [mode, setMode] = useState<string | null>(null)
  const [chosenView, setView] = useState<SessionView | null>(null)
  const [resumeId, setResumeId] = useState("")
  const [prompt, setPrompt] = useState("")
  const [query, setQuery] = useState("")
  const [chosenWorkspace, setWorkspace] = useState<string | null>(null)
  const [carry, setCarry] = useState(true)

  const now = Date.now()
  const machineFilter = machines.some((machine) => machine.id === filters.machine) ? filters.machine : "all"
  const agentFilter = agentIds.includes(filters.agent) ? filters.agent : "all"
  const browserMachine = machines.find((machine) => machine.id === (machineFilter === "all" ? LOCAL : machineFilter))
  const needsFoldersUpdate = (machine: string) => foundQueries.byMachine[machine]?.needsUpdate === true || projectQueries.byMachine[machine]?.needsUpdate === true
  const rows: Record<string, ProjectRow[] | undefined> = {}
  const foundRows: Record<string, ProjectRow[] | undefined> = {}
  for (const machine of machines) {
    rows[machine.id] = projectQueries.byMachine[machine.id]?.data
    if (machine.online && !needsFoldersUpdate(machine.id)) {
      const used = new Set(rows[machine.id]?.map((row) => placeKey(machine.id, row.directory)))
      foundRows[machine.id] = foundQueries.byMachine[machine.id]?.data?.filter((row) => !used.has(placeKey(machine.id, row.directory)))
    }
  }
  const browser = useFolderBrowser(
    browserMachine,
    browserMachine ? needsFoldersUpdate(browserMachine.id) || Boolean(browserMachine.needsUpdate) : false,
    query,
    rows[browserMachine?.id ?? LOCAL] ?? [],
  )
  const browsing = browser.browsing
  const projects = mergeProjects(machines, rows)
  const found = mergeProjects(machines, foundRows)
  const groups = projectGroups(projects, found, machineFilter, agentFilter, browsing ? "" : query, now)
  const shown = groups.flatMap((group) => group.rows)
  const total = projects.length + found.length
  const requested = picked && placeKey(picked.machine, picked.row.directory)
  const pickedRow = picked && (picked.pathQuery
    ? picked.row
    : rows[picked.machine]?.find((row) => placeKey(picked.machine, row.directory) === requested) ?? picked.row)
  const pickedProject = picked && pickedRow ? mergeProjects(machines, { [picked.machine]: [pickedRow] })[0] : undefined
  const listKey = (item: MergedProject) => `${item.lastUsed ? "recent" : "found"}\0${item.key}`
  const pathProject = browsing && browser.preview
    ? mergeProjects(machines, { [browser.machineId]: [browser.preview] })[0]
    : undefined
  const project = pathProject ?? pickedProject ?? shown.find((item) => listKey(item) === selectedProject) ?? shown.find((item) => item.places.some(canStart)) ?? shown[0]
  const place =
    project?.places.find((item) => item.key === chosenPlace) ??
    project?.places.find(canStart) ??
    project?.places[0]
  const row = place?.row
  const registered = Boolean(place && rows[place.machine]?.some((entry) => entry.available && placeKey(place.machine, entry.directory) === place.key && entry.folder_id === place.row.folder_id))
  const placeQuery = place ? projectQueries.byMachine[place.machine] : undefined
  const contentsLoading = Boolean(placeQuery?.pending && !row?.folder_id)
  const newest = row?.launches[0]
  // What this browser last started here wins over the folder's newest launch.
  const remembered = row ? loadStartChoices(row.directory) : {}
  const harnessId = harness ?? defaultAgent(remembered, row?.launches ?? []).harness
  const modes = row ? modeChoices(row) : ["default"]
  const modeValue = mode && modes.includes(mode) ? mode : remembered.mode && modes.includes(remembered.mode) && (!harness || harness === remembered.harness) ? remembered.mode : newest?.mode || "default"
  const view = chosenView ?? remembered.view ?? "chat"
  const machineName = place?.machineName ?? "this machine"
  const startable = place ? canStart(place) : false

  const accounts = useQuery({ ...agentsQuery(place?.machine ?? ""), enabled: Boolean(place?.online) })
  const pastSessions = useQuery({
    queryKey: ["agent-sessions", place?.machine, row?.directory, harnessId, modeValue],
    queryFn: async () => {
      const body = await api<{ sessions: AgentSession[] }>("/api/agent-sessions", {
        machine: place!.machine,
        query: { directory: row!.directory, harness: harnessId, mode: modeValue },
      })
      return body.sessions ?? []
    },
    enabled: view === "chat" && startable && (registered || Boolean(row?.launches.length)),
    retry: false,
  })

  // Workspace: the folder itself, a new worktree, or a live worktree of this project (its path).
  const worktrees = useQuery({ ...worktreesQuery(place?.machine ?? ""), enabled: Boolean(place?.online && row?.available) })
  const worktreesNeedUpdate = routeMissing(worktrees.error)
  const machineSessions = useReplicatedQueries(sessionsQuery).byMachine[place?.machine ?? ""]?.data ?? []
  const folder = row?.directory.replace(/[\\/]+$/, "")
  const othersHere = machineSessions.filter((item) => item.state?.running && !item.ended_at && projectDirectory(item).replace(/[\\/]+$/, "") === folder)
  const projectWorktrees = row ? (worktrees.data?.worktrees ?? []).filter((item) => !item.orphaned && item.branch && worktreeFolderInProject(item, row.directory)) : []
  const canWorktree = Boolean(row?.git?.head) && worktrees.data?.git === true && !resumeId
  const showWorkspace = Boolean(row?.git) || worktreesNeedUpdate
  const workspace = !canWorktree ? "folder" : chosenWorkspace ?? remembered.workspace ?? (othersHere.length > 0 ? "new" : "folder")
  const dirty = row?.git?.dirty ?? 0
  const continued = projectWorktrees.find((item) => item.path === workspace)

  const start = useStartSession()
  // A worktree that could not be made is reported under Workspace; nothing started.
  const worktreeError = start.error instanceof ApiError && start.error.status === 422 ? start.error.message : undefined

  function chooseProject(next: MergedProject) {
    setPicked(undefined)
    setSelectedProject(listKey(next))
    resetProject()
  }

  function pickFolder(machine: string, row: ProjectRow) {
    resetProject()
    setPicked({ machine, row, pathQuery: query })
  }

  function changeFilters(next: typeof filters) {
    setFilters(next)
    saveProjectFilters(next)
    resetProject()
    setPicked(undefined)
    setSelectedProject(null)
  }

  function resetProject() {
    setWorkspace(null)
    setChosenPlace(null)
    setHarness(null)
    setMode(null)
    setResumeId("")
  }

  // The agent stays as chosen; mode and conversation belong to the folder on that machine.
  function choosePlace(next: string) {
    setChosenPlace(next)
    setWorkspace(null)
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
      start.mutate({
        machine: place.machine,
        directory: row.directory,
        harness: harnessId,
        mode: modeValue,
        view,
        agentSessionId: resumeId || undefined,
        prompt,
        worktree: !canWorktree ? undefined : workspace === "folder" ? "" : workspace,
        carry: workspace === "new" && dirty > 0 && carry,
      })
    }
  }

  function submitShortcut(event: KeyboardEvent<HTMLFormElement>) {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault()
      event.currentTarget.requestSubmit()
    }
  }

  // One reason at a time, most blocking first.
  const chatUnavailableHint = chatUnavailableReason(pastSessions.error, machineName)
  const account = accounts.data?.find((item) => item.harness === harnessId)
  const placeError = placeQuery?.error
  let hint: string | undefined
  if (online.length === 0) hint = "No machine is online."
  else if (place && !place.online) hint = `${machineName} is offline. Choose another machine for ${project?.name}.`
  else if (row && !row.available) hint = `${row.unavailable_reason || "This folder is missing"} on ${machineName}. Choose another machine.`
  else if (placeQuery?.needsUpdate && !picked) hint = `Update Agentpack on ${machineName} to see its projects.`
  else if (placeError && !picked) hint = `Couldn't load projects from ${machineName}: ${placeError.message}`
  else if (!browsing && !projectQueries.firstLoadPending && total === 0 && !row) hint = "Open a folder to start a session."
  else if (chatUnavailableHint) hint = chatUnavailableHint
  else if (view === "chat" && pastSessions.error && !signInError(pastSessions.error)) hint = `Couldn't load conversations: ${(pastSessions.error as Error).message}`
  const blocked = (browsing && !pathProject) || !startable || start.isPending || browser.creation.isPending || (view === "chat" && Boolean(chatUnavailableHint))
  const failedMachines = machines.filter((machine) => machine.online && projectQueries.byMachine[machine.id]?.error)

  const machineItems = [...machines].sort((a, b) =>
    Number(b.id === LOCAL) - Number(a.id === LOCAL) || Number(b.online) - Number(a.online) || a.name.localeCompare(b.name),
  ).map((machine) => {
    const old = needsFoldersUpdate(machine.id) || machine.needsUpdate
    return {
      value: machine.id,
      label: machine.name,
      detail: (
        <>
          <StatusGlyph state={!machine.online ? "offline" : old ? "needsUpdate" : "running"} className="size-2" />
          {!machine.online ? "offline" : old ? "needs update" : rows[machine.id]?.length ?? 0}
        </>
      ),
      group: machine.online ? undefined : "Offline",
      disabled: browsing && (!machine.online || old),
    }
  })
  const projectRow = (item: MergedProject) => {
    const best = item.places.find(canStart)
    const source = best ?? item.places[0]!
    const agents = [...new Set(item.places.flatMap((entry) => entry.row.launches.map((launch) => harnessName(launch.harness))))]
    return (
      <SourceRow
        key={item.key}
        type="button"
        selected={item === project || Boolean(requested && item.places.some((entry) => entry.key === requested))}
        title={item.places.map((entry) => `${entry.machineName}: ${entry.row.directory}`).join("\n")}
        onClick={() => chooseProject(item)}
        className={cn("h-auto flex-col items-stretch gap-0 py-(--s3) max-[760px]:h-auto", !best && "opacity-50")}
      >
        <span className="flex min-w-0 items-baseline gap-(--s3)">
          <span className="min-w-0 flex-1 truncate font-semibold">{item.name}</span>
          {item.lastUsed > 0 && <span className="shrink-0 text-(length:--fs-xs) opacity-70">{shortAge(new Date(item.lastUsed), now)}</span>}
        </span>
        <span className="truncate text-(length:--fs-xs) opacity-70">{projectLocation(item, machineFilter)}</span>
        <span className="truncate text-(length:--fs-xs) opacity-70">
          {[...agents, source.row.error ? "Packages unavailable" : plural(source.row.packages.length, "package")].join(" · ")}
        </span>
      </SourceRow>
    )
  }

  return (
    <form onSubmit={submit} onKeyDown={submitShortcut} className="contents">
      <header className="flex min-w-0 items-center gap-(--s4) border-b-[0.5px] border-(--sep) px-(--s6) py-(--s5)">
        <div className="min-w-0 flex-1">
          <SheetTitle>New session</SheetTitle>
          <SheetDescription>Pick a project or any folder, then the machine to run it on.</SheetDescription>
        </div>
      </header>

      <div className="grid min-h-0 grid-cols-[280px_minmax(0,1fr)] max-[760px]:grid-cols-1 max-[760px]:grid-rows-[minmax(0,180px)_minmax(0,1fr)]">
        <FolderBrowser
          query={query}
          setQuery={setQuery}
          browser={browser}
          pick={pickFolder}
          filters={(
            <div className="flex min-w-0 items-center gap-(--s2) px-(--s4)">
              <PopupSelect
                label="Filter by machine"
                disabled={browser.creation.isPending}
                value={browsing ? browserMachine?.id ?? LOCAL : machineFilter}
                onValueChange={(machine) => changeFilters({ ...filters, machine })}
                items={browsing ? machineItems : [
                  { value: "all", label: "All machines", detail: String(projects.length) },
                  ...machineItems,
                ]}
              />
              {!browsing && <>
                <PopupSelect label="Filter by agent" value={agentFilter} onValueChange={(agent) => changeFilters({ ...filters, agent })} items={[
                  { value: "all", label: "All agents" },
                  ...agentIds.map((value) => ({ value, label: harnessName(value) })),
                ]} />
                {(machineFilter !== "all" || agentFilter !== "all" || query.trim()) && (
                  <span className="shrink-0 text-(length:--fs-xs) text-(--fg2)">{shown.length} of {total}</span>
                )}
              </>}
            </div>
          )}
        >
          <SourceList aria-label="Projects" className="flex-1 px-(--s3)">
            {groups.map((group) => group.rows.length > 0 && (
              <Fragment key={group.label}>
                <GroupHeader>{group.label}</GroupHeader>
                {group.rows.map(projectRow)}
              </Fragment>
            ))}
            {!shown.length && (
              <p className="px-(--s3) text-(--fg2)">
                {projectQueries.firstLoadPending || foundQueries.firstLoadPending
                  ? "Loading projects…"
                  : query.trim() ? `No project matches “${query}”.` : total ? "No project matches these filters." : "No projects yet"}
              </p>
            )}
          </SourceList>
          {failedMachines.length > 0 && (
            <p className="border-t-[0.5px] border-(--sep) px-(--s4) py-(--s3) text-(length:--fs-xs) text-(--fg2)">
              {failedMachines.map((machine) => `${machine.name}: ${projectQueries.byMachine[machine.id]?.needsUpdate ? "Needs update" : "projects unavailable"}`).join(" · ")}
            </p>
          )}
        </FolderBrowser>

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

              {view === "chat" && !resumeId && (
                <Choice label="First message" note="optional, sent when the agent is ready">
                  <textarea
                    aria-label="First message"
                    rows={3}
                    value={prompt}
                    onChange={(event) => setPrompt(event.currentTarget.value)}
                    placeholder="Describe the task"
                    className="w-full resize-y rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--field) px-(--s3) py-(--s2) text-(length:--fs) outline-none focus:ring-2 focus:ring-(--accent-soft)"
                  />
                </Choice>
              )}

              <Choice label="Agent">
                <div role="group" aria-label="Agent" className="flex flex-wrap gap-(--s2)">
                  {agentIds.map((id) => {
                    const used = row.launches.find((launch) => launch.harness === id)
                    return (
                      <Button key={id} type="button" variant={id === harnessId ? "primary" : "default"} aria-pressed={id === harnessId} title={used ? `Last used ${relativeTime(new Date(used.last_used), now)}` : undefined} onClick={() => { setHarness(id); setResumeId("") }}>
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

              {showWorkspace && (
                <Choice label="Workspace">
                  <SegmentedControl
                    value={[workspace === "folder" ? "folder" : "new"]}
                    onValueChange={(value) => (value[0] === "folder" || value[0] === "new") && setWorkspace(value[0])}
                    aria-label="Workspace"
                    className="w-full"
                  >
                    <SegmentedControlItem value="folder" className="flex-1">
                      This folder
                    </SegmentedControlItem>
                    <SegmentedControlItem value="new" className="flex-1" disabled={!canWorktree} title={worktreesNeedUpdate ? `Update Agentpack on ${machineName} to use worktrees` : resumeId ? "A resumed conversation keeps its folder" : undefined}>
                      {worktreesNeedUpdate ? "New worktree · Needs update" : "New worktree"}
                    </SegmentedControlItem>
                  </SegmentedControl>
                  {canWorktree && projectWorktrees.length > 0 && (
                    <div role="group" aria-label="Existing worktrees" className="flex flex-wrap gap-(--s2)">
                      {projectWorktrees.map((item) => (
                        <Button key={item.path} type="button" size="sm" variant={item.path === workspace ? "primary" : "default"} aria-pressed={item.path === workspace} title={item.path} onClick={() => setWorkspace(item.path)}>
                          Continue in {item.branch}
                        </Button>
                      ))}
                    </div>
                  )}
                  <p className="text-(length:--fs-sm) text-(--fg2)">
                    {workspace === "new"
                      ? `Branch ap/${branchSlug(prompt)} from ${row.git?.branch || "the current commit"}.`
                      : continued
                        ? `Shares ${continued.branch} with ${plural(continued.sessions.length, "other session")}.`
                        : "Runs in the folder itself."}
                    {othersHere.length > 0 && ` ${plural(othersHere.length, "other agent")} ${othersHere.length === 1 ? "is" : "are"} working in ${project.name}.`}
                  </p>
                  {workspace === "new" && dirty > 0 && (
                    <label className="flex items-center gap-(--s3) text-(length:--fs-sm)">
                      <input type="checkbox" checked={carry} onChange={(event) => setCarry(event.currentTarget.checked)} className="accent-(--accent)" />
                      Bring {plural(dirty, "uncommitted change")}
                      <span className="text-(--fg3)">· untracked files stay behind</span>
                    </label>
                  )}
                  {worktreeError && <FieldError>{worktreeError}</FieldError>}
                </Choice>
              )}

              {pathProject && row && !row.folder_id ? (
                <p className="text-(length:--fs-sm) text-(--fg2)">Open this folder to see what it loads.</p>
              ) : contentsLoading ? (
                <p className="text-(--fg2)">Loading what this folder loads on {machineName}…</p>
              ) : (
                <LoadedContents project={row} mode={modeValue} machine={place?.machine ?? LOCAL} machineName={machineName} />
              )}

              {view === "chat" && startable && (
                <Choice label="Conversation">
                  {pastSessions.isLoading ? (
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
        {place && !hint && account?.installed && !account.signedIn && (
          <p className="text-(length:--fs-sm) text-(--fg2)">
            {harnessName(harnessId)} isn't signed in on {machineName}.{" "}
            <Link to="/machines/$machine" params={{ machine: place.machine }} className="text-(--accent) underline-offset-2 hover:underline">Sign in…</Link>
            {view === "chat" ? " or start anyway: the chat asks you to sign in." : null}
          </p>
        )}
        {start.error && !worktreeError && <FieldError>{start.error.message}</FieldError>}
        <div className="flex min-w-0 items-center gap-(--s4) max-[760px]:flex-col max-[760px]:items-stretch">
          <p className="min-w-0 flex-1 truncate text-(length:--fs-sm) text-(--fg2)" aria-live="polite">
            {project && place && (
              <>
                <b className="font-semibold text-(--fg)">{harnessName(harnessId)}</b> · {modeValue} · {view === "chat" ? "Chat" : "Terminal"} in{" "}
                <b className="font-semibold text-(--fg)">{project.name}</b> on <b className="font-semibold text-(--fg)">{machineName}</b>
                {resumeId && " · resumes a conversation"}
                {workspace === "new" ? " · new worktree" : continued ? ` · in ${continued.branch}` : ""}
                {view === "chat" && !resumeId && prompt.trim() && " · sends the first message"}
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
function LoadedContents({ project, mode, machine, machineName }: { project: ProjectRow; mode: string; machine: string; machineName: string }) {
  const credentials = useQuery(connectionsQuery(useEventsConnected())).data?.credentials ?? []
  const choosable = project.mcp.some((server) => server.source !== "plugin" && loginsFor(server.url ?? "", credentials).length > 1)
  const effect = modeEffect(project, mode)
  const off = project.packages.filter((pkg) => effect.offPackages.has(pkg.module)).length + project.mcp.filter((server) => server.disabled || effect.offMCP.has(server.name)).length
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
                off={server.disabled || effect.offMCP.has(server.name)}
                status={server.status}
                tone={server.status === "pinned" ? "ok" : server.status === "unpinned" ? "warn" : undefined}
                mode={mode}
                login={loginsFor(server.url ?? "", credentials).length > 0 && <LoginPicker project={project} server={server} logins={loginsFor(server.url ?? "", credentials)} machine={machine} />}
              />
            ))}
          </ContentsGroup>
        </div>
      )}
      {choosable && <p className="text-(length:--fs-xs) text-(--fg3)">{`Logins you pick are saved for ${project.name} on ${machineName} and used by its new sessions.`}</p>}
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

function ContentsRow({ name, detail, tag, status, tone, off, mode, login }: { name: string; detail: string; tag: string; status: string; tone?: "ok" | "warn"; off: boolean; mode: string; login?: ReactNode }) {
  return (
    <li data-off={off || undefined} className="flex min-w-0 items-center gap-(--s4) border-t-[0.5px] border-(--sep-soft) px-(--s5) py-(--s3) first:border-t-0 data-off:opacity-50">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-(--s3)">
          <span className={cn("truncate font-medium", off && "line-through")}>{name}</span>
          {tag && <Tag>{tag}</Tag>}
        </div>
        {detail && <div className="truncate font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2)">{detail}</div>}
      </div>
      {login && !off ? login : <Tag className={cn(!off && tone === "ok" && "text-(--ok-word)", !off && tone === "warn" && "text-(--warn-word)")}>{off ? `off in ${mode}` : status}</Tag>}
    </li>
  )
}

function Tag({ className, children }: { className?: string; children: ReactNode }) {
  return <span className={cn("shrink-0 rounded-(--r-row) border-[0.5px] border-(--sep) px-(--s2) text-(length:--fs-xs) whitespace-nowrap text-(--fg2)", className)}>{children}</span>
}

// The login this project's sessions use for one server: the project's pick, else the first login (the default).
// A pick is saved on the project's machine at once; it never blocks Start.
function LoginPicker({ project, server, logins, machine }: { project: ProjectRow; server: ProjectRow["mcp"][number]; logins: Connection[]; machine: string }) {
  const queryClient = useQueryClient()
  const save = useMutation<unknown, Error, string>({
    mutationFn: (credential) => api("/api/projects/mcp-login", { machine, method: "PUT", body: { directory: project.directory, url: server.url, credential } }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: projectsQuery(machine).queryKey }),
    onError: (error) => {
      if (!routeMissing(error)) hud.error(`Couldn't save the ${server.name} login for ${project.name}: ${errorText(error)}`)
    },
  })
  // An older peer has no route to save a pick: its sessions keep the default.
  const outdated = routeMissing(save.error)
  const plugin = server.source === "plugin"
  if (plugin && server.project_login && server.login && !logins.some((login) => login.id === server.login)) {
    return <Tag className="text-(--warn-word)">Selected login unavailable</Tag>
  }
  const value = save.isPending ? save.variables || logins[0]!.id : logins.some((login) => login.id === server.login) ? server.login! : logins[0]!.id
  const chosen = logins.find((login) => login.id === value)!
  if (plugin && !(server.project_login && server.login) && !chosen.plugins.includes(server.module ?? "")) {
    return <Tag className="text-(--warn-word)">Choose login in Project Settings</Tag>
  }
  const expired = chosen.status === "needs_login"
  const reconnect = (
    <Button type="button" size="sm" variant="primary" onClick={() => void signIn({ url: chosen.match, label: chosen.label, return_to: location.pathname + location.search })}>Reconnect…</Button>
  )
  // Plugin permissions are edited in Project Settings.
  if (plugin || logins.length === 1) {
    return (
      <span className="flex shrink-0 items-center gap-(--s3) text-(length:--fs-sm) text-(--fg2)">
        {expired && reconnect}
        <LoginGlyph login={expired ? "needs_login" : "connected"} />
        <span className={cn(expired && "text-(--danger-word)")}>{expired ? `${chosen.label} · Needs sign-in` : chosen.label}</span>
      </span>
    )
  }
  return (
    <span className="flex shrink-0 items-center gap-(--s3)">
      {expired && reconnect}
      <span className={cn("w-44 [&_[data-slot=select-trigger]]:w-full", expired && "[&_[data-slot=select-trigger]]:text-(--danger-word)")}>
        <PopupSelect
          label={`${server.name} login for ${project.name}`}
          value={value}
          disabled={save.isPending || outdated}
          onValueChange={(next) => save.mutate(next === logins[0]!.id ? "" : next)}
          items={logins.map((login, index) => ({
            value: login.id,
            label: login.status === "needs_login" ? `${login.label} · Needs sign-in` : login.label,
            detail: index === 0 ? "Default" : undefined,
          }))}
        />
      </span>
      {outdated && <span className="text-(length:--fs-xs) text-(--fg2)">Needs update</span>}
    </span>
  )
}
