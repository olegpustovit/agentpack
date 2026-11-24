import { Fragment, lazy, Suspense, useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react"
import { loadProjectFilters, loadStartChoices, saveProjectFilters } from "./choices"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { FieldError, PopupSelect } from "@/components/kit/forms"
import { StatusGlyph } from "@/components/kit/status-dot"
import { Kbd, Spinner } from "@/components/kit/feedback"
import { GroupHeader, SegmentedControl, SegmentedControlItem, SourceList, SourceRow } from "@/components/kit/list"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button, buttonVariants } from "@/components/ui/button"
import { useMachineQueries, useMachines, useReplicatedQueries } from "@/features/machines/queries"
import { agentsQuery, type AgentAccount } from "@/features/agents/queries"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { api, ApiError, errorText, LOCAL, routeMissing } from "@/lib/api"
import type { AgentSession, ProjectRow } from "@/lib/api-types"
import { harnessName, plural, relativeTime, shortAge } from "@/lib/format"
import { cn } from "@/lib/utils"
import { agentIds, canStart, defaultAgent, mergeProjects, modeChoices, modeEffect, offlineProjects, placeKey, placeLabel, projectGroups, projectLocation, stubRow, type MergedProject, type SessionView } from "./rules"
import { useStartSession, type StartInput } from "./start"
import { useEventsConnected } from "@/features/events/use-events"
import { LoginGlyph } from "@/features/library/mcp-inspector"
import { connectionsQuery, loginsFor, type Connection } from "@/features/settings/connections-model"
import { hud } from "@/lib/hud"
import { branchSlug, projectDirectory, worktreeFolderInProject, worktreesQuery } from "./git"
import { foundQuery, projectsQuery, selectedProjectQuery, sessionsQuery } from "./queries"
import { FolderBrowser, useFolderBrowser } from "./folder-browser"
import { ProjectHistory } from "./project-history"
import { ProjectSignInStatus, useProjectSignIn } from "./project-sign-in"
import { RowSheet, type SheetState } from "./session-sheets"

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)
const AgentSignIn = lazy(() => import("@/features/agents/sign-in").then((module) => ({ default: module.SignIn })))

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
  initialPrompt,
  onAccepted,
  onOpenChange,
}: {
  open: boolean
  initialMachine?: string
  initialDirectory?: string
  initialPrompt?: string
  onAccepted?: (input: StartInput) => void
  onOpenChange: (open: boolean) => void
}) {
  const start = useStartSession({ onAccepted: (input) => {
    onAccepted?.(input)
    if (!onAccepted) onOpenChange(false)
  } })
  return (
    <Sheet open={open} onOpenChange={(next) => { if (!start.isPending) { start.reset(); onOpenChange(next) } }}>
      {open && (
        <SheetContent className="h-[min(640px,calc(100dvh-var(--bar-h)-var(--s6)))] w-[min(900px,calc(100vw-var(--s6)*2))] max-w-none sm:max-w-none max-[760px]:w-full grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 max-[760px]:h-[calc(100dvh-var(--safe-t)-var(--s4))] max-[760px]:pb-0">
          <NewSessionForm initialMachine={initialMachine} initialDirectory={initialDirectory} initialPrompt={initialPrompt} start={start} />
        </SheetContent>
      )}
    </Sheet>
  )
}

function NewSessionForm({ initialMachine, initialDirectory, initialPrompt, start }: {
  initialMachine?: string; initialDirectory?: string; initialPrompt?: string; start: ReturnType<typeof useStartSession>
}) {
  const machines = useMachines()
  const projectQueries = useMachineQueries(projectsQuery)
  const foundQueries = useMachineQueries(foundQuery)
  const sessionQueries = useReplicatedQueries(sessionsQuery).byMachine
  const online = machines.filter((machine) => machine.online)
  // Default to the requested folder or the newest startable project.
  const [filters, setFilters] = useState(() => {
    const saved = loadProjectFilters()
    return { ...saved, machine: initialMachine ?? saved.machine }
  })
  const [picked, setPicked] = useState<{ machine: string; row: ProjectRow; browsed?: boolean } | undefined>(() =>
    initialMachine && initialDirectory ? { machine: initialMachine, row: stubRow(initialDirectory) } : undefined,
  )
  const [selectedProject, setSelectedProject] = useState<string | null>(null)
  const [chosenPlace, setChosenPlace] = useState<string | null>(null)
  const [harness, setHarness] = useState<string | null>(null)
  const [mode, setMode] = useState<string | null>(null)
  const [chosenView, setView] = useState<SessionView | null>(null)
  const [resume, setResume] = useState(false)
  const [resumeId, setResumeId] = useState("")
  const [prompt, setPrompt] = useState(initialPrompt ?? "")
  const [query, setQuery] = useState("")
  const [folderPath, setFolderPath] = useState("~/")
  const [browseFolders, setBrowseFolders] = useState(false)
  const [chooseProjectOnPhone, setChooseProjectOnPhone] = useState(false)
  const isPhone = useIsPhone()
  const [chosenWorkspace, setWorkspace] = useState<string | null>(null)
  const [carry, setCarry] = useState(true)
  const [history, setHistory] = useState(false)
  const [renameSheet, setRenameSheet] = useState<SheetState | null>(null)
  const [signInTarget, setSignInTarget] = useState<{ machine: string; name: string; account: AgentAccount }>()
  const worktreeControl = useRef<HTMLDivElement>(null)
  const worktreeErrorId = useId()
  const check = useMutation({ mutationFn: start.check })

  const now = Date.now()
  const machineFilter = machines.some((machine) => machine.id === filters.machine) ? filters.machine : "all"
  const agentFilter = agentIds.includes(filters.agent) ? filters.agent : "all"
  const browserMachine = machines.find((machine) => machine.id === machineFilter)
  const browsing = browseFolders && Boolean(browserMachine)
  const needsFoldersUpdate = (machine: string) => foundQueries.byMachine[machine]?.needsUpdate === true || projectQueries.byMachine[machine]?.needsUpdate === true
  const rows: Record<string, ProjectRow[] | undefined> = {}
  const foundRows: Record<string, ProjectRow[] | undefined> = {}
  for (const machine of machines) {
    rows[machine.id] = projectQueries.byMachine[machine.id]?.data
    if (machine.online && !needsFoldersUpdate(machine.id)) {
      const used = new Set(rows[machine.id]?.map((row) => placeKey(machine.id, row.directory)))
      foundRows[machine.id] = foundQueries.byMachine[machine.id]?.data?.filter((row) => !used.has(placeKey(machine.id, row.directory)))
    } else if (history && !machine.online && !rows[machine.id]?.length) {
      rows[machine.id] = offlineProjects(sessionQueries[machine.id]?.data ?? [])
    }
  }
  const browser = useFolderBrowser(
    browserMachine,
    browserMachine ? needsFoldersUpdate(browserMachine.id) || Boolean(browserMachine.needsUpdate) : false,
    browsing,
    folderPath,
    rows[browserMachine?.id ?? ""] ?? [],
  )
  const projects = mergeProjects(machines, rows)
  const found = mergeProjects(machines, foundRows)
  const groups = projectGroups(projects, found, machineFilter, agentFilter, query, now)
  const shown = groups.flatMap((group) => group.rows)
  const total = projects.length + found.length
  const requested = picked && placeKey(picked.machine, picked.row.directory)
  const pickedRow = picked && (picked.browsed
    ? picked.row
    : rows[picked.machine]?.find((row) => placeKey(picked.machine, row.directory) === requested) ?? picked.row)
  const pickedProject = picked && pickedRow ? mergeProjects(machines, { [picked.machine]: [pickedRow] })[0] : undefined
  const listKey = (item: MergedProject) => `${item.lastUsed ? "recent" : "found"}\0${item.key}`
  const project = pickedProject ?? shown.find((item) => listKey(item) === selectedProject) ?? shown.find((item) => item.places.some(canStart)) ?? shown[0]
  const place =
    project?.places.find((item) => item.key === chosenPlace) ??
    project?.places.find(canStart) ??
    project?.places[0]
  const summary = place?.row
  // The suggested project is visible as soon as the sheet opens. Wait for a
  // choice before asking a Mac to inspect that particular project on disk.
  const explicit = Boolean(project && ((project === pickedProject && picked?.browsed) || listKey(project) === selectedProject || chosenPlace === place?.key))
  const projectDetails = useQuery({
    ...selectedProjectQuery(place?.machine ?? "", summary?.directory ?? ""),
    enabled: !history && Boolean(explicit && place?.online && summary?.details_deferred),
  })
  const row = projectDetails.data ?? summary
  const registered = Boolean(place && rows[place.machine]?.some((entry) => entry.available && placeKey(place.machine, entry.directory) === place.key && entry.folder_id === place.row.folder_id))
  const placeQuery = place ? projectQueries.byMachine[place.machine] : undefined
  const contentsLoading = Boolean(placeQuery?.pending && !row?.folder_id || projectDetails.isLoading)
  const newest = row?.launches[0]
  // What this browser last started here wins over the folder's newest launch.
  const remembered = row ? loadStartChoices(row.directory) : {}
  const accounts = useQuery({ ...agentsQuery(place?.machine ?? ""), enabled: !history && Boolean(place?.online) })
  const usual = defaultAgent(remembered, row?.launches ?? []).harness
  const harnessId = harness ?? (accounts.data?.find((item) => item.harness === usual)?.installed === false
    ? accounts.data.find((item) => item.installed && agentIds.includes(item.harness))?.harness ?? usual : usual)
  const modes = row ? modeChoices(row) : ["default"]
  const modeValue = mode && modes.includes(mode) ? mode : remembered.mode && modes.includes(remembered.mode) && (!harness || harness === remembered.harness) ? remembered.mode : newest?.mode || "default"
  const view = chosenView ?? remembered.view ?? "chat"
  const resuming = view === "chat" && resume
  const branchName = `ap/${branchSlug(view === "chat" ? prompt : "")}`
  const machineName = place?.machineName ?? "this machine"
  const launchMachineName = start.input ? machines.find((machine) => machine.id === start.input?.machine)?.name ?? start.input.machine : machineName
  const startable = Boolean(place?.online && row?.available)
  const account = accounts.data?.find((item) => item.harness === harnessId)
  const pastSessions = useQuery({
    queryKey: ["agent-sessions", place?.machine, row?.directory, harnessId, modeValue, explicit],
    queryFn: async () => {
      const body = await api<{ sessions: AgentSession[] }>("/api/agent-sessions", {
        machine: place!.machine,
        query: { directory: row!.directory, harness: harnessId, mode: modeValue, ...(explicit && { explicit: "1" }) },
      })
      return body.sessions ?? []
    },
    enabled: !history && view === "chat" && startable && account?.installed !== false && (registered || Boolean(row?.launches.length)),
    refetchOnWindowFocus: !explicit,
    refetchOnReconnect: !explicit,
    retry: false,
  })
  const resumeConversation = resuming ? pastSessions.data?.find((item) => item.id === resumeId) : undefined
  const resumeTitle = resumeConversation?.title?.trim() || resumeConversation?.id

  // Workspace: the folder itself, a new worktree, or a live worktree of this project (its path).
  const worktrees = useQuery({
    ...worktreesQuery(place?.machine ?? "", explicit && summary?.details_deferred ? row?.directory : undefined),
    enabled: !history && Boolean(place?.online && row?.available && (!summary?.details_deferred || explicit)),
  })
  const worktreesNeedUpdate = routeMissing(worktrees.error)
  const machineSessions = sessionQueries[place?.machine ?? ""]?.data ?? []
  const folder = row?.directory.replace(/[\\/]+$/, "")
  const othersHere = machineSessions.filter((item) => item.state?.running && !item.ended_at && projectDirectory(item).replace(/[\\/]+$/, "") === folder)
  const projectWorktrees = row ? (worktrees.data?.worktrees ?? []).filter((item) => !item.orphaned && item.branch && worktreeFolderInProject(item, row.directory)) : []
  const canWorktree = Boolean(row?.git?.head) && worktrees.data?.git === true && !resuming
  const showWorkspace = Boolean(row?.git) || worktreesNeedUpdate
  const workspace = !canWorktree ? "folder" : chosenWorkspace ?? remembered.workspace ?? (othersHere.length > 0 ? "new" : "folder")
  const dirty = row?.git?.dirty ?? 0
  const continued = projectWorktrees.find((item) => item.path === workspace)

  const worktreeError = showWorkspace && start.input?.worktree && start.error instanceof ApiError && start.error.status === 422 ? start.error.message : undefined
  useEffect(() => {
    if (worktreeError) worktreeControl.current?.focus()
  }, [worktreeError])
  const showProjectPicker = isPhone && !history && (chooseProjectOnPhone || !row)
  const unavailable = account?.installed === false
  const mutable = !start.isPending && !start.outcomeUnknown

  function chooseProject(next: MergedProject) {
    setPicked(undefined)
    setSelectedProject(listKey(next))
    resetProject()
    setChooseProjectOnPhone(false)
  }

  function pickFolder(machine: string, row: ProjectRow) {
    resetProject()
    setPicked({ machine, row, browsed: true })
    setChooseProjectOnPhone(false)
  }

  function changeFilters(next: typeof filters) {
    if (isPhone && !history) setChooseProjectOnPhone(true)
    if (filters.machine !== next.machine) setFolderPath("~/")
    if (next.machine === "all") setBrowseFolders(false)
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
    if (!start.outcomeUnknown) start.reset()
  }

  // The agent stays as chosen; mode and conversation belong to the folder on that machine.
  function choosePlace(next: string) {
    setChosenPlace(next)
    setWorkspace(null)
    setMode(null)
    setResumeId("")
    start.reset()
  }

  function chooseView(value: unknown[]) {
    const next = value[0]
    if (next !== "chat" && next !== "terminal") return
    setView(next)
    start.reset()
  }

  function launch(sendOnResume = false) {
    if (!history && place && row && !blocked && (!sendOnResume || (resumeConversation && prompt.trim()))) {
      start.mutate({
        machine: place.machine,
        directory: resumeConversation?.cwd || row.directory,
        harness: harnessId,
        mode: modeValue,
        view,
        agentSessionId: resumeConversation?.id,
        prompt: view === "chat" ? prompt : undefined,
        sendOnResume,
        worktree: !canWorktree ? undefined : workspace === "folder" ? "" : workspace,
        carry: workspace === "new" && dirty > 0 && carry,
      })
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    launch()
  }

  function submitShortcut(event: KeyboardEvent<HTMLFormElement>) {
    if (!history && event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault()
      event.currentTarget.requestSubmit()
    }
  }

  // One reason at a time, most blocking first.
  const chatUnavailableHint = chatUnavailableReason(pastSessions.error, machineName)
  const placeError = placeQuery?.error
  let hint: string | undefined
  if (online.length === 0) hint = "No machine is online."
  else if (place && !place.online) hint = `${machineName} is offline. Choose another machine for ${project?.name}.`
  else if (row && !row.available) hint = `${row.unavailable_reason || "This folder is missing"} on ${machineName}. Choose another machine.`
  else if (placeQuery?.needsUpdate && !picked) hint = `Update Agentpack on ${machineName} to see its projects.`
  else if (placeError && !picked) hint = `Couldn't load projects from ${machineName}: ${placeError.message}`
  else if (!browsing && !projectQueries.firstLoadPending && total === 0 && !row) hint = "Open a folder to start a session."
  else if (unavailable) hint = `${harnessName(harnessId)} is not installed on ${machineName}. Choose an installed agent.`
  else if (chatUnavailableHint) hint = chatUnavailableHint
  else if (view === "chat" && pastSessions.error && !signInError(pastSessions.error)) hint = `Couldn't load conversations: ${(pastSessions.error as Error).message}`
  const blocked = !mutable || browser.creation.isPending || showProjectPicker || !startable || accounts.isLoading || unavailable ||
    (browsing && !picked?.browsed) || (resuming && !resumeConversation) || (view === "chat" && Boolean(chatUnavailableHint))
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
    }
  })
  const projectRow = (item: MergedProject) => {
    const best = item.places.find(canStart)
    const source = best ?? item.places[0]!
    const agents = [...new Set(item.places.flatMap((entry) => entry.row.launches.map((launch) => harnessName(launch.harness))))]
    const packages = source.row.details_deferred ? [] : [source.row.error ? "Packages unavailable" : plural(source.row.packages.length, "package")]
    return (
      <SourceRow
        key={item.key}
        type="button"
        selected={item === project || Boolean(requested && item.places.some((entry) => entry.key === requested))}
        title={item.places.map((entry) => `${entry.machineName}: ${entry.row.directory}`).join("\n")}
        onClick={() => chooseProject(item)}
        className={cn("h-auto flex-col items-stretch gap-0 px-(--s5) py-(--s3) max-[760px]:h-auto", !best && "opacity-50")}
      >
        <span className="flex min-w-0 items-baseline gap-(--s3)">
          <span className="min-w-0 flex-1 truncate font-semibold">{item.name}</span>
          {item.lastUsed > 0 && <span className="shrink-0 text-(length:--fs-xs) opacity-70">{shortAge(new Date(item.lastUsed), now)}</span>}
        </span>
        <span className="truncate text-(length:--fs-xs) opacity-70">{projectLocation(item, machineFilter)}</span>
        <span className="truncate text-(length:--fs-xs) opacity-70">
          {[...agents, ...packages].join(" · ")}
        </span>
      </SourceRow>
    )
  }

  return <>
    <form onSubmit={submit} onKeyDown={submitShortcut} className="contents">
      <header className="flex min-w-0 items-center justify-between gap-(--s3) border-b-[0.5px] border-(--sep) px-(--dlg-pad) py-(--s5)">
        <div className="min-w-0 flex-1">
          <SheetTitle>{history ? "Project history" : "New session"}</SheetTitle>
          <SheetDescription className="sr-only">{history ? "Open or rename saved sessions." : "Choose where to run the session and what to start."}</SheetDescription>
        </div>
        <SegmentedControl aria-label="Project sessions" disabled={!mutable || browser.creation.isPending} value={[history ? "history" : "start"]} onValueChange={([value]) => { if (value) setHistory(value === "history") }}>
          <SegmentedControlItem value="start">Launch</SegmentedControlItem>
          <SegmentedControlItem value="history" className="flex-1">History</SegmentedControlItem>
        </SegmentedControl>
      </header>

      <fieldset disabled={!mutable} className={cn("grid min-h-0 min-w-0 grid-cols-[280px_minmax(0,1fr)] grid-rows-[minmax(0,1fr)] max-[760px]:grid-cols-1", history && "max-[760px]:grid-rows-[34dvh_auto] max-[760px]:overflow-y-auto")}>
        <FolderBrowser
          query={browsing ? folderPath : query}
          setQuery={browsing ? (path) => {
            setFolderPath(path)
            if (picked?.browsed) setPicked(undefined)
          } : (value) => {
            if (isPhone && !history) setChooseProjectOnPhone(true)
            setQuery(value)
          }}
          setBrowsing={setBrowseFolders}
          browser={browser}
          pick={pickFolder}
          className={cn(!history && !showProjectPicker && "max-[760px]:hidden")}
          done={row && !history ? () => {
            if (!picked?.browsed) setBrowseFolders(false)
            setChooseProjectOnPhone(false)
          } : undefined}
          filters={(
            <div className="flex min-w-0 items-center gap-(--s2) px-(--s5)">
              <PopupSelect
                label="Filter by machine"
                value={machineFilter}
                onValueChange={(machine) => changeFilters({ ...filters, machine })}
                items={[
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
          <SourceList aria-label="Projects" className="flex-1 px-(--s4)">
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

        <div className={cn("flex min-h-0 min-w-0 flex-col gap-(--s6) overflow-y-auto px-(--dlg-pad) py-(--s5)", showProjectPicker && "hidden")}>
          {project && history ? (
            <div className="flex min-w-0 flex-col gap-(--s4)">
              <h2 className="truncate text-(length:--fs-lg) font-semibold">{project.name}</h2>
              <ProjectHistory key={project.key} places={project.places} agent={agentFilter} onRename={(row) => setRenameSheet({ kind: "rename", row })} />
            </div>
          ) : project && place && row ? (
            <>
              <section aria-label="Where" className="flex min-w-0 flex-col gap-(--s2)">
                <h2 className="font-semibold">Where</h2>
                <div className="flex flex-wrap items-center gap-(--s3)">
                  <h3 className="min-w-0 flex-1 wrap-anywhere text-(length:--fs-lg) font-semibold">{project.name}</h3>
                  <Button type="button" size="sm" className="min-[761px]:hidden" onClick={() => setChooseProjectOnPhone(true)}>Choose project…</Button>
                  <PopupSelect
                    label="Machine"
                    value={place.key}
                    onValueChange={choosePlace}
                    items={project.places.map((entry) => ({ value: entry.key, label: placeLabel(entry, project.places), disabled: !canStart(entry) }))}
                  />
                </div>
                <p className="wrap-anywhere font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2) max-[760px]:max-h-10 max-[760px]:overflow-y-auto">{row.directory}</p>
              </section>

              <section aria-label="What" className="flex min-w-0 flex-col gap-(--s4)">
                <h2 className="font-semibold">What</h2>
                <div className="flex flex-wrap items-center gap-(--s3)">
                  <SegmentedControl value={[view]} onValueChange={chooseView} aria-label="Session view">
                    <SegmentedControlItem value="chat" disabled={Boolean(chatUnavailableHint)}>Chat</SegmentedControlItem>
                    <SegmentedControlItem value="terminal">Terminal</SegmentedControlItem>
                  </SegmentedControl>
                  {view === "chat" && (
                    <SegmentedControl aria-label="Conversation mode" value={[resume ? "resume" : "new"]} onValueChange={([value]) => {
                      if (value) setResume(value === "resume")
                    }}>
                      <SegmentedControlItem value="new">New conversation</SegmentedControlItem>
                      <SegmentedControlItem value="resume">Resume</SegmentedControlItem>
                    </SegmentedControl>
                  )}
                </div>

                <Choice label="Agent">
                  <div role="group" aria-label="Agent" className="flex flex-wrap gap-(--s2)">
                    {agentIds.map((id) => {
                      const used = row.launches.find((launch) => launch.harness === id)
                      const missing = accounts.data?.some((entry) => entry.harness === id && !entry.installed)
                      return (
                        <Button key={id} type="button" variant={id === harnessId ? "primary" : "default"} aria-pressed={id === harnessId}
                          disabled={missing} className="h-auto min-h-(--ctl-h) whitespace-normal"
                          title={used && !missing ? `Last used ${relativeTime(new Date(used.last_used), now)}` : undefined}
                          onClick={() => { setHarness(id); setResumeId("") }}>
                          {harnessName(id)}
                          {missing ? <span className="text-(length:--fs-xs)">· not installed on {machineName}</span>
                            : used && <span className="text-(length:--fs-xs) opacity-70">{shortAge(new Date(used.last_used), now)}</span>}
                        </Button>
                      )
                    })}
                  </div>
                  {account?.installed && !account.signedIn && !account.checkRequired && (
                    <div className="flex flex-wrap items-center gap-(--s2) text-(length:--fs-sm) text-(--fg2)">
                      <p>{harnessName(harnessId)} isn't signed in on {machineName}.
                        {view === "chat" && !resuming && " You can also sign in after starting the chat."}
                      </p>
                      {account.methods.length > 0 ? (
                        <Button type="button" size="sm" onClick={() => setSignInTarget({ machine: place.machine, name: machineName, account })}>Sign in…</Button>
                      ) : <p>Run {harnessName(harnessId)} on {machineName} to sign in.</p>}
                    </div>
                  )}
                </Choice>

                <Choice label="Mode" note="chooses what loads below">
                  <SegmentedControl value={[modeValue]} onValueChange={([value]) => {
                    if (typeof value === "string") { setMode(value); setResumeId("") }
                  }} aria-label="Mode" className="w-full">
                    {modes.map((name) => (
                      <SegmentedControlItem key={name} value={name} className="min-w-0 flex-1 truncate">{name}</SegmentedControlItem>
                    ))}
                  </SegmentedControl>
                </Choice>

                {resuming && (
                  <Choice label="Conversation">
                    {pastSessions.isLoading ? (
                      <span role="status" className="text-(--fg2)">Loading conversations…</span>
                    ) : pastSessions.data?.length ? (
                      <div role="radiogroup" aria-label="Past conversations" className="flex max-h-48 flex-col overflow-y-auto">
                        {pastSessions.data.map((item) => (
                          <label key={item.id} className="flex min-h-(--row-h) min-w-0 items-center gap-(--s4) rounded-(--r-row) px-(--s3) hover:bg-(--hover)">
                            <input type="radio" name="conversation" value={item.id} checked={resumeId === item.id} onChange={() => setResumeId(item.id)} className="accent-(--accent)" />
                            <span className="min-w-0 flex-1 wrap-anywhere">{item.title?.trim() || item.id}</span>
                          </label>
                        ))}
                      </div>
                    ) : (
                      <p className="text-(--fg2)">{signInError(pastSessions.error) ? "Sign in to list past conversations." : pastSessions.isError ? "Past conversations are unavailable." : "No conversations to resume for this project and agent."}</p>
                    )}
                  </Choice>
                )}

                {view === "chat" && (
                  <Choice label={resuming ? "Unsent draft" : "First message"} note={resuming ? "Resume keeps this draft in Work" : "optional, sent when the agent is ready"}>
                    <textarea
                      aria-label={resuming ? "Unsent draft" : "First message"}
                      rows={3}
                      value={prompt}
                      onChange={(event) => setPrompt(event.currentTarget.value)}
                      placeholder="Describe the task"
                      className="w-full resize-y rounded-(--r-ctl) border-[0.5px] border-(--control-edge) bg-(--field) px-(--s3) py-(--s2) text-(length:--fs) outline-none focus:ring-2 focus:ring-(--accent)"
                    />
                  </Choice>
                )}
              </section>

              {showWorkspace && !resuming && (
                <div role="group" aria-label="Worktree" tabIndex={-1} ref={worktreeControl}
                  aria-invalid={worktreeError ? true : undefined} aria-describedby={worktreeError ? worktreeErrorId : undefined}>
                  <Choice label="Worktree">
                    <SegmentedControl
                      value={workspace === "folder" || workspace === "new" ? [workspace] : []}
                      onValueChange={([value]) => {
                        if (value === "folder" || value === "new") { setWorkspace(value); start.reset() }
                      }}
                      aria-label="Workspace"
                      className="w-full"
                    >
                      <SegmentedControlItem value="folder" className="flex-1">
                        This folder
                      </SegmentedControlItem>
                      <SegmentedControlItem value="new" className="flex-1" disabled={!canWorktree} title={worktreesNeedUpdate ? `Update Agentpack on ${machineName} to use worktrees` : undefined}>
                        {worktreesNeedUpdate ? "New worktree · Needs update" : "New worktree"}
                      </SegmentedControlItem>
                    </SegmentedControl>
                    {canWorktree && projectWorktrees.length > 0 && (
                      <div role="group" aria-label="Existing worktrees" className="flex flex-wrap gap-(--s2)">
                        {projectWorktrees.map((item) => (
                          <Button key={item.path} type="button" size="sm" variant={item.path === workspace ? "primary" : "default"} aria-pressed={item.path === workspace} title={item.path} onClick={() => { setWorkspace(item.path); start.reset() }}>
                            Continue branch {item.branch} (existing)
                          </Button>
                        ))}
                      </div>
                    )}
                    <p className="text-(length:--fs-sm) text-(--fg2)">
                      {workspace === "new"
                        ? `Branch ${branchName} from ${row.git?.branch || "the current commit"}.`
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
                    {worktreeError && <FieldError id={worktreeErrorId}>{worktreeError}</FieldError>}
                  </Choice>
                </div>
              )}

              {contentsLoading ? (
                <p className="text-(--fg2)">Loading what this folder loads on {machineName}…</p>
              ) : row?.details_deferred ? (
                <p className="text-(length:--fs-sm) text-(--fg2)">Select this project to see what it loads.</p>
              ) : (
                <LoadedContents key={place.key} project={row} mode={modeValue} machine={place.machine} machineName={machineName} />
              )}
            </>
          ) : (
            <p className="text-(--fg2)">{history ? "Choose a project." : hint ?? "Choose a project."}</p>
          )}
        </div>
      </fieldset>

      <footer className="flex min-w-0 flex-col gap-(--s2) border-t-[0.5px] border-(--sep) bg-(--sidebar) px-(--dlg-pad) py-(--s4) max-[760px]:pb-[calc(var(--s4)+var(--safe-b))] max-[760px]:[&_[data-slot=sheet-actions]]:flex-row max-[760px]:[&_[data-slot=sheet-actions]]:flex-wrap">
        {history ? (
          <SheetActions pinned={false} cancelLabel="Done">{null}</SheetActions>
        ) : start.isPending ? (
          <p role="status" className="flex items-center gap-(--s3)"><Spinner /> Starting session on {launchMachineName}…</p>
        ) : start.outcomeUnknown ? (
          <div className="flex flex-col gap-(--s2)">
            <FieldError>Launch outcome unknown. The session may have started.</FieldError>
            {check.data === false && <p role="status">No matching session yet. Check again before starting another.</p>}
            {check.error && <FieldError>{errorText(check.error)}</FieldError>}
            <SheetActions pinned={false} cancelLabel="Close">
              <Button type="button" disabled={check.isPending} onClick={() => check.mutate()}>
                {check.isPending ? "Checking…" : `Check sessions on ${launchMachineName}`}
              </Button>
              {check.data === false && start.input?.view === "terminal" && (
                <Link to="/sessions" search={{ q: launchMachineName, tab: "all" }} target="_blank" rel="noopener" className={buttonVariants()}>
                  View sessions on {launchMachineName}
                </Link>
              )}
            </SheetActions>
          </div>
        ) : (
          <>
            {place && hint && <p className="wrap-anywhere text-(length:--fs-sm) text-(--fg2)">{hint}</p>}
            {start.error && !worktreeError && <FieldError>{start.needsTerminalUpdate
              ? `Needs update · Install a newer Agentpack on ${machineName} to launch a terminal.`
              : start.error.message}</FieldError>}
            {row && !showProjectPicker && (
              <div aria-label="Launch summary" role="status" className="max-h-[24dvh] min-w-0 overflow-y-auto wrap-anywhere text-(length:--fs-sm) text-(--fg2)">
                <strong className="text-(--fg)">{harnessName(harnessId)} {view === "chat" ? "chat" : "terminal"} on {machineName}</strong>
                {" · "}{modeValue} · {resumeConversation?.cwd || continued?.path || row.directory}
                {resuming ? resumeTitle ? ` · Resume “${resumeTitle}”` : " · Choose a conversation"
                  : workspace === "new" ? ` · New worktree ${branchName}` : continued ? ` · Continue branch ${continued.branch}` : ""}
                {view === "chat" && (resuming ? (prompt.trim() ? " · Resume leaves the draft unsent" : " · Sends no message") : prompt.trim() ? " · Sends the first message" : " · Sends no message")}
              </div>
            )}
            {!blocked && resuming && prompt.trim() && (
              <details open className="min-w-0 text-(length:--fs-sm) text-(--fg2)">
                <summary className="cursor-pointer">Message queued by Resume and send</summary>
                <pre className="max-h-20 overflow-y-auto py-(--s2) whitespace-pre-wrap wrap-anywhere">{prompt.trim()}</pre>
              </details>
            )}
            <SheetActions pinned={false}>
              {!showProjectPicker && (
                <>
                  <Button type="submit" variant="primary" disabled={blocked}>
                    {resuming ? "Resume" : "Start session"}
                    <Kbd aria-hidden className="text-current opacity-70">{isMac ? "⌘↵" : "Ctrl+↵"}</Kbd>
                  </Button>
                  {resuming && prompt.trim() && <Button type="button" disabled={blocked} onClick={() => launch(true)}>Resume and send</Button>}
                </>
              )}
            </SheetActions>
          </>
        )}
      </footer>
    </form>
    <RowSheet sheet={renameSheet} onClose={() => setRenameSheet(null)} finalFocus />
    {signInTarget && (
      <Sheet open onOpenChange={(open) => { if (!open) setSignInTarget(undefined) }}>
        <SheetContent>
          <SheetTitle>Sign in to {harnessName(signInTarget.account.harness)}</SheetTitle>
          <SheetDescription>Agent on {signInTarget.name}</SheetDescription>
          <Suspense fallback={<Spinner />}>
            <AgentSignIn machine={signInTarget.machine} machineName={signInTarget.name} harness={signInTarget.account.harness}
              methods={signInTarget.account.methods}
              onSignedIn={() => {
                if (startable && (registered || row?.launches.length)) void pastSessions.refetch()
              }} />
          </Suspense>
          <SheetActions cancelLabel="Back to setup">{null}</SheetActions>
        </SheetContent>
      </Sheet>
    )}
  </>
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
  const connections = useQuery(connectionsQuery(useEventsConnected()))
  const credentials = connections.data?.credentials ?? []
  const signIn = useProjectSignIn(() => connections.refetch({ throwOnError: true }))
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
                login={loginsFor(server.url ?? "", credentials).length > 0 && <LoginPicker project={project} server={server} logins={loginsFor(server.url ?? "", credentials)} machine={machine} signIn={signIn} />}
              />
            ))}
          </ContentsGroup>
        </div>
      )}
      <ProjectSignInStatus signIn={signIn} />
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
    <li data-off={off || undefined} className="flex min-w-0 flex-wrap items-center gap-(--s4) border-t-[0.5px] border-(--sep-soft) px-(--s5) py-(--s3) first:border-t-0 data-off:opacity-50">
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
function LoginPicker({ project, server, logins, machine, signIn }: {
  project: ProjectRow; server: ProjectRow["mcp"][number]; logins: Connection[]; machine: string
  signIn: ReturnType<typeof useProjectSignIn>
}) {
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
    <Button type="button" size="sm" variant="primary" disabled={Boolean(signIn.pending)}
      onClick={() => void signIn.connect(server.name, chosen.match, chosen.label)}>Reconnect…</Button>
  )
  // Plugin permissions are edited in Project Settings.
  if (plugin || logins.length === 1) {
    return (
      <span className="flex min-w-0 flex-wrap items-center gap-(--s3) text-(length:--fs-sm) text-(--fg2)">
        {expired && reconnect}
        <LoginGlyph login={expired ? "needs_login" : "connected"} />
        <span className={cn(expired && "text-(--danger-word)")}>{expired ? `${chosen.label} · Needs sign-in` : chosen.label}</span>
      </span>
    )
  }
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-(--s3)">
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
