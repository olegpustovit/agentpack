import { Fragment, lazy, Suspense, useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react"
import { loadProjectFilters, loadStartChoices, saveProjectFilters } from "./choices"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { FieldError, PopupSelect } from "@/components/kit/forms"
import { StatusGlyph } from "@/components/kit/status-dot"
import { Kbd, Spinner } from "@/components/kit/feedback"
import { FilterButton, FilterChip, FilterFacet, FilterToken } from "@/components/kit/filters"
import { GroupHeader, SegmentedControl, SegmentedControlItem, SourceList, SourceRow } from "@/components/kit/list"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { ChevronRight, FolderSearch, History, Plus, SquareSlash } from "lucide-react"
import { isFolderPath } from "./folder-path"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button, buttonVariants } from "@/components/ui/button"
import { useMachineQueries, useMachines, useReplicatedQueries } from "@/features/machines/queries"
import { agentsQuery, type AgentAccount } from "@/features/agents/queries"
import { applyHint, atQuery, commandSpans, filterAtHints, filterSlashHints, moveHint, slashQuery, type ComposerHint } from "@/features/conversation/composer-hints"
import { ComposerHintsMenu } from "@/features/conversation/composer-hints-menu"
import { configRole, parseConfigOptions, type ConfigOption } from "@/features/conversation/config-options"
import { agentSnapshotQuery } from "@/features/conversation/queries"
import { AttachmentRow } from "@/features/conversation/attachment-row"
import { imageFilesFromClipboard, imageFilesFromList } from "@/features/conversation/composer-attach"
import { loadImageSize, revokePreview } from "@/features/conversation/upload-blob"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { api, ApiError, errorText, LOCAL, routeMissing } from "@/lib/api"
import type { AgentSession, ProjectRow } from "@/lib/api-types"
import { harnessName, plural, shortAge } from "@/lib/format"
import { cn } from "@/lib/utils"
import { agentIds, canStart, defaultAgent, mergeProjects, modeChoices, modeEffect, offlineProjects, placeKey, placeLabel, projectGroups, projectLocation, stubRow, type FirstImage, type MergedProject, type SessionView } from "./rules"
import { useStartSession, type StartInput } from "./start"
import { useEventsConnected } from "@/features/events/use-events"
import { LoginGlyph } from "@/features/library/mcp-inspector"
import { connectionsQuery, loginsFor, type Connection } from "@/features/settings/connections-model"
import { hud } from "@/lib/hud"
import { branchSlug, projectDirectory, worktreeFolderInProject, worktreesQuery } from "./git"
import { foundQuery, projectFilesQuery, projectsQuery, selectedProjectQuery, sessionsQuery } from "./queries"
import { FolderBrowser, useFolderBrowser } from "./folder-browser"
import { ProjectHistory } from "./project-history"
import { ProjectSignInStatus, useProjectSignIn } from "./project-sign-in"
import { RowSheet, type SheetState } from "./session-sheets"

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)
const AgentSignIn = lazy(() => import("@/features/agents/sign-in").then((module) => ({ default: module.SignIn })))
const automaticChoice = "\0"
// The gateway's limits on a first message.
const maxImages = 32
const maxImageBytes = 20 * 1024 * 1024

type SheetImage = FirstImage & { size: number; previewUrl: string }

function readBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result).split(",")[1] ?? "")
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

async function readImage(file: File): Promise<SheetImage> {
  const [{ width, height }, data] = await Promise.all([loadImageSize(file), readBase64(file)])
  // A blob: preview, because the gateway's CSP refuses data: images.
  return { name: file.name, width, height, data, size: file.size, previewUrl: URL.createObjectURL(file) }
}

function launchChoices(option: Extract<ConfigOption, { type: "select" }>, label: string) {
  const last = option.items.find((item) => item.value === option.value)?.label
  return [
    { value: automaticChoice, label: `${label}: automatic`, detail: last ? `Last used: ${last}` : undefined },
    ...option.items.map((item) => ({ ...item, disabled: Boolean(item.disabledReason), detail: item.disabledReason })),
  ]
}

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
  // Closing the sheet discards these, as it discards the typed text.
  const [images, setImages] = useState<SheetImage[]>([])
  const [imageError, setImageError] = useState<string>()
  const [reading, setReading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const imagesRef = useRef(images)
  useEffect(() => { imagesRef.current = images }, [images])
  useEffect(() => () => { for (const image of imagesRef.current) revokePreview(image.previewUrl) }, [])
  const [query, setQuery] = useState("")
  const [folderPath, setFolderPath] = useState("~/")
  const [browseFolders, setBrowseFolders] = useState(false)
  // An explicit browsing choice overrides the machine suggested by the project filter.
  const [browseOn, setBrowseOn] = useState<string | null>(null)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [chooseProjectOnPhone, setChooseProjectOnPhone] = useState(false)
  const isPhone = useIsPhone()
  const [chosenWorkspace, setWorkspace] = useState<string | null>(null)
  const [carry, setCarry] = useState(true)
  const [history, setHistory] = useState(false)
  const [renameSheet, setRenameSheet] = useState<SheetState | null>(null)
  const [signInTarget, setSignInTarget] = useState<{ machine: string; name: string; account: AgentAccount }>()
  const [modelPicks, setModelPicks] = useState<Record<string, string>>({})
  const [effortPicks, setEffortPicks] = useState<Record<string, string>>({})
  const [hintIndex, setHintIndex] = useState(0)
  const [hintsClosed, setHintsClosed] = useState(false)
  const [caret, setCaret] = useState<{ draft: string; at: number }>()
  const worktreeControl = useRef<HTMLDivElement>(null)
  const promptRef = useRef<HTMLTextAreaElement>(null)
  const backdropRef = useRef<HTMLDivElement>(null)
  const worktreeErrorId = useId()
  const hintsId = useId()
  const check = useMutation({ mutationFn: start.check })

  const now = Date.now()
  const machineFilter = machines.some((machine) => machine.id === filters.machine) ? filters.machine : "all"
  const agentFilter = agentIds.includes(filters.agent) ? filters.agent : "all"
  const filteredMachine = machines.find((machine) => machine.id === machineFilter)
  const browserMachine = machines.find((machine) => machine.id === browseOn && machine.online)
    ?? filteredMachine
    ?? machines.find((machine) => machine.id === LOCAL && machine.online)
    ?? online[0]
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
  const explicit = Boolean(project && (project === pickedProject || listKey(project) === selectedProject || chosenPlace === place?.key))
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
    if (worktreeError) worktreeControl.current?.querySelector<HTMLElement>('[role="combobox"]')?.focus()
  }, [worktreeError])
  const showProjectPicker = isPhone && !history && (chooseProjectOnPhone || !row)
  const unavailable = account?.installed === false
  const mutable = !start.isPending && !start.outcomeUnknown
  const chatUnavailableHint = chatUnavailableReason(pastSessions.error, machineName)
  const snapshotEnabled = !history && view === "chat" && startable && !unavailable && !chatUnavailableHint && Boolean(place?.machine && row?.directory && harnessId)
  const snapshotQuery = useQuery({
    ...agentSnapshotQuery(
      place?.machine ?? "",
      row ? { directory: row.directory, harness: harnessId, mode: modeValue } : undefined,
      { modelFallback: true },
    ),
    enabled: snapshotEnabled,
  })
  const activeSnapshot = snapshotEnabled ? snapshotQuery.data : undefined
  const configOptions = parseConfigOptions(activeSnapshot?.config).filter((option) => option.type === "select")
  const modelOption = configOptions.find((option) => configRole(option) === "model")
  const effortOption = configOptions.find((option) => configRole(option) === "effort")
  const optionScope = place && row ? `${place.machine}\0${row.directory}\0${harnessId}\0${modeValue}` : ""
  const pickedModel = optionScope ? modelPicks[optionScope] : undefined
  const pickedEffort = optionScope ? effortPicks[optionScope] : undefined
  const chosenModel = pickedModel && modelOption?.items.some((item) => item.value === pickedModel && !item.disabledReason) ? pickedModel : undefined
  const chosenEffort = pickedEffort && effortOption?.items.some((item) => item.value === pickedEffort && !item.disabledReason) ? pickedEffort : undefined
  const rawCommands = activeSnapshot?.commands?.availableCommands ?? []
  const hasAgentCommands = filterSlashHints("/", rawCommands, { includeGateway: false }).length > 0
  const caretAt = caret?.draft === prompt ? caret.at : prompt.length
  const beforeCaret = prompt.slice(0, caretAt)
  const slashHints = view === "chat" ? filterSlashHints(beforeCaret, rawCommands, { includeGateway: false }) : []
  const fileDirectory = resumeConversation?.cwd || continued?.path || row?.directory || ""
  const atActive = view === "chat" && atQuery(beforeCaret) !== undefined && slashHints.length === 0
  const projectFiles = useQuery({
    ...projectFilesQuery(place?.machine ?? "", fileDirectory),
    enabled: !history && startable && atActive && !hintsClosed && Boolean(place?.machine && fileDirectory),
  })
  const atHints = atActive ? filterAtHints(beforeCaret, projectFiles.data?.files ?? []) : []
  const composerHints = hintsClosed ? [] : slashHints.length > 0 ? slashHints : atHints
  const hintTitle = slashHints.length > 0 ? "Commands" : "Project files"
  const activeHint = Math.min(hintIndex, Math.max(composerHints.length - 1, 0))
  const spans = view === "chat" && prompt.includes("/") ? commandSpans(prompt, rawCommands, { includeGateway: false }) : []
  const showModelAbsenceCue = snapshotEnabled && !snapshotQuery.isPending && !modelOption

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
    if (filters.machine !== next.machine) {
      setBrowseOn(null)
      setFolderPath("~/")
    }
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

  function pickHint(hint: ComposerHint) {
    setHintIndex(0)
    const head = applyHint(beforeCaret, hint)
    const tail = prompt.slice(caretAt)
    const next = head + (tail.startsWith(" ") ? tail.slice(1) : tail)
    setPrompt(next)
    setCaret({ draft: next, at: head.length })
    requestAnimationFrame(() => {
      promptRef.current?.focus()
      promptRef.current?.setSelectionRange(head.length, head.length)
    })
  }

  function openSlashHints() {
    const next = `${prompt}${prompt && !/\s$/.test(prompt) ? " " : ""}/`
    setPrompt(next)
    setCaret({ draft: next, at: next.length })
    setHintIndex(0)
    setHintsClosed(false)
    requestAnimationFrame(() => {
      promptRef.current?.focus()
      promptRef.current?.setSelectionRange(next.length, next.length)
    })
  }

  async function attachImages(files: File[]) {
    if (history || view !== "chat" || showProjectPicker || !row || !mutable || reading || files.length === 0) return
    const allowed = imageFilesFromList(files)
    start.reset()
    setImageError(allowed.length < files.length ? "Only PNG, JPEG, GIF and WebP images can be attached." : undefined)
    const bytes = [...images, ...allowed].reduce((sum, item) => sum + item.size, 0)
    if (images.length + allowed.length > maxImages || bytes > maxImageBytes) {
      setImageError("Attach up to 32 images, 20 MiB in total.")
      return
    }
    setReading(true)
    for (const file of allowed) {
      await readImage(file).then(
        (image) => setImages((current) => [...current, image]),
        () => setImageError(`Couldn't read ${file.name}.`),
      )
    }
    setReading(false)
  }

  function removeImage(image: SheetImage) {
    revokePreview(image.previewUrl)
    setImages((current) => current.filter((item) => item !== image))
    setImageError(undefined)
    start.reset()
  }

  function launch(sendOnResume = false) {
    if (!history && place && row && !blocked && (!sendOnResume || (resumeConversation && hasMessage))) {
      start.mutate({
        machine: place.machine,
        directory: resumeConversation?.cwd || row.directory,
        harness: harnessId,
        mode: modeValue,
        view,
        agentSessionId: resumeConversation?.id,
        prompt: view === "chat" ? prompt : undefined,
        images: view === "chat" && images.length > 0 ? images.map(({ name, width, height, data }) => ({ name, width, height, data })) : undefined,
        sendOnResume,
        model: view === "chat" ? chosenModel : undefined,
        effort: view === "chat" ? chosenEffort : undefined,
        worktree: !canWorktree ? undefined : workspace === "folder" ? "" : workspace,
        carry: workspace === "new" && dirty > 0 && carry,
      })
    }
  }

  // With a message typed, resuming sends it; "Resume only" keeps it as a draft.
  function submit(event: FormEvent) {
    event.preventDefault()
    launch(resuming && hasMessage)
  }

  function submitShortcut(event: KeyboardEvent<HTMLFormElement>) {
    if (event.nativeEvent.isComposing || event.keyCode === 229) return
    if (!history && event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault()
      event.currentTarget.requestSubmit()
    }
  }

  // One reason at a time, most blocking first.
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
  const hasMessage = prompt.trim() !== "" || images.length > 0
  const blocked = !mutable || reading || browser.creation.isPending || showProjectPicker || !startable || accounts.isLoading || unavailable ||
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
      offline: !machine.online,
    }
  })
  const filtered = machineFilter !== "all" || agentFilter !== "all"
  // Faceted counts: what each choice would list alongside the other filter and the search.
  const countOf = (machine: string, agent: string) => projectGroups(projects, found, machine, agent, query, now).reduce((sum, group) => sum + group.rows.length, 0)
  const machineCount = (machine: string) => countOf(machine, agentFilter)
  const agentCount = (agent: string) => countOf(machineFilter, agent)
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
    <form
      onSubmit={submit}
      onKeyDown={submitShortcut}
      onDragOver={(event) => { if (event.dataTransfer.types.includes("Files")) event.preventDefault() }}
      onDrop={(event) => {
        if (!event.dataTransfer.types.includes("Files")) return
        event.preventDefault()
        void attachImages(Array.from(event.dataTransfer.files))
      }}
      className="contents"
    >
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
            // A path switches the same field to folder browsing.
            if (browserMachine && !history && isFolderPath(value)) {
              setFolderPath(value)
              setBrowseFolders(true)
              return
            }
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
          tools={!browsing && (
            <Popover open={filtersOpen} onOpenChange={setFiltersOpen}>
              <PopoverTrigger render={<FilterButton active={filtered} />} aria-label={filtered ? "Filters, on" : "Filters"} title="Filter by machine or agent" />
              <PopoverContent align="end" sideOffset={6} aria-label="Project filters"
                className="w-80 gap-(--s5) rounded-(--r-pop) bg-(--raised) p-(--s5) text-(length:--fs) shadow-(--e-pop) ring-0">
                {machines.length > 1 && (
                  <FilterFacet label="Machine" active={machineFilter !== "all"} onClear={() => changeFilters({ ...filters, machine: "all" })}>
                    {machineItems.map((item) => (
                      <FilterChip key={item.value} pressed={machineFilter === item.value} count={machineCount(item.value)}
                        onClick={() => changeFilters({ ...filters, machine: machineFilter === item.value ? "all" : item.value })}>
                        {item.offline && <span aria-hidden className="size-1.5 rounded-full border border-dashed border-(--fg3)" />}
                        {item.label}
                      </FilterChip>
                    ))}
                  </FilterFacet>
                )}
                <FilterFacet label="Used with" active={agentFilter !== "all"} onClear={() => changeFilters({ ...filters, agent: "all" })}>
                  {agentIds.filter((agent) => agent === agentFilter || agentCount(agent) > 0).map((agent) => (
                    <FilterChip key={agent} pressed={agentFilter === agent} count={agentCount(agent)}
                      onClick={() => changeFilters({ ...filters, agent: agentFilter === agent ? "all" : agent })}>
                      {harnessName(agent)}
                    </FilterChip>
                  ))}
                </FilterFacet>
              </PopoverContent>
            </Popover>
          )}
          filters={browsing ? (
            <div className="flex min-w-0 items-center gap-(--s3) px-(--s5) text-(length:--fs-sm) text-(--fg2)">
              <span className="shrink-0">Browsing on</span>
              <PopupSelect label="Browse folders on" value={browserMachine?.id ?? ""} onValueChange={(machine) => {
                setFolderPath("~/")
                setBrowseOn(machine)
                if (picked?.browsed) {
                  setPicked(undefined)
                  resetProject()
                }
              }} items={machineItems.filter((item) => !item.offline).map(({ value, label }) => ({ value, label }))} />
            </div>
          ) : (filtered || query.trim()) ? (
            <div role="group" aria-label="Active filters" className="flex min-w-0 flex-wrap items-center gap-(--s2) px-(--s5)">
              {machineFilter !== "all" && <FilterToken label="Machine" value={filteredMachine?.name ?? machineFilter} onOpen={() => setFiltersOpen(true)} onRemove={() => changeFilters({ ...filters, machine: "all" })} />}
              {agentFilter !== "all" && <FilterToken label="Used with" value={harnessName(agentFilter)} onOpen={() => setFiltersOpen(true)} onRemove={() => changeFilters({ ...filters, agent: "all" })} />}
              <span className="ms-auto flex items-center gap-(--s2) text-(length:--fs-xs) text-(--fg2) tabular-nums">
                {`${shown.length} of ${total}`}
                {filtered && <button type="button" onClick={() => changeFilters({ ...filters, machine: "all", agent: "all" })} className="rounded-(--r-xs) font-medium text-(--accent) hover:underline">Clear</button>}
              </span>
            </div>
          ) : null}
        >
          <SourceList aria-label="Projects" className="flex-1 px-(--s4)">
            {groups.map((group) => group.rows.length > 0 && (
              <Fragment key={group.label}>
                <GroupHeader>{group.label}</GroupHeader>
                {group.rows.map(projectRow)}
              </Fragment>
            ))}
            {!history && browserMachine && (
              <SourceRow type="button" onClick={() => setBrowseFolders(true)} className="mt-(--s3) text-(--fg2)">
                <FolderSearch aria-hidden className="size-4" />
                <span className="min-w-0 flex-1 truncate">{`Browse folders on ${browserMachine.name}…`}</span>
              </SourceRow>
            )}
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
              {/* The project, then one card: the task with how to run it. Everything else appears only when it matters. */}
              <header className="flex min-w-0 items-start gap-(--s4)">
                <div className="flex min-w-0 flex-1 flex-col gap-(--s1)">
                  <h2 className="truncate text-(length:--fs-lg) font-semibold">{project.name}</h2>
                  <p className="truncate text-(length:--fs-sm) text-(--fg2)" title={row.directory}>
                    {row.directory}{row.git?.branch && <span className="text-(--fg3)">{` · ${row.git.branch}`}{dirty > 0 && ` · ${plural(dirty, "change")}`}</span>}
                  </p>
                </div>
                <Button type="button" size="sm" className="min-[761px]:hidden" onClick={() => setChooseProjectOnPhone(true)}>Change…</Button>
                {project.places.length > 1 ? (
                  <PopupSelect label="Machine" value={place.key} onValueChange={choosePlace}
                    items={project.places.map((entry) => ({ value: entry.key, label: placeLabel(entry, project.places), disabled: !canStart(entry) }))} />
                ) : <span className="flex h-(--ctl-h) shrink-0 items-center text-(length:--fs-sm) text-(--fg2)">{machineName}</span>}
              </header>

              <div className="flex min-w-0 flex-col gap-(--s3)">
                <div className="relative flex min-w-0 flex-col rounded-(--r-pop) bg-(--field) shadow-[inset_0_0_0_.5px_var(--control-edge)] transition-shadow duration-(--dur-1) ease-(--ease) has-[textarea:focus]:shadow-[inset_0_0_0_1px_var(--accent),0_0_0_3px_var(--accent-tint)]">
                  {resuming && (
                    <div className="flex min-w-0 items-center gap-(--s3) border-b-[0.5px] border-(--sep-soft) px-(--s5) py-(--s3) text-(length:--fs-sm)">
                      <History aria-hidden className="size-3.5 shrink-0 text-(--fg2)" />
                      <span className="min-w-0 flex-1 truncate">{resumeTitle ? <>Resuming <strong>{resumeTitle}</strong></> : "Choose a conversation to resume"}</span>
                      <button type="button" onClick={() => { setResume(false); setResumeId("") }} className="shrink-0 text-(--accent) hover:underline">New conversation</button>
                    </div>
                  )}
                  {view === "chat" ? (
                    <div className="relative">
                      {spans.some((span) => span.command) && (
                        <div ref={backdropRef} aria-hidden className="pointer-events-none absolute inset-0 overflow-hidden px-(--s5) pt-(--s4) text-(length:--fs) wrap-break-word whitespace-pre-wrap text-transparent">
                          {spans.map((span, index) => span.command ? <mark key={index} data-command className="rounded-[3px] bg-(--accent-soft) text-transparent">{span.text}</mark> : span.text)}{" "}
                        </div>
                      )}
                      <textarea
                        ref={promptRef}
                        aria-label={resuming ? "Message to send after resuming" : "First message"}
                        rows={3}
                        value={prompt}
                        aria-controls={composerHints.length > 0 ? hintsId : undefined}
                        aria-activedescendant={composerHints.length > 0 ? `${hintsId}-${activeHint}` : undefined}
                        onScroll={(event) => { if (backdropRef.current) backdropRef.current.scrollTop = event.currentTarget.scrollTop }}
                        onSelect={(event) => setCaret({ draft: event.currentTarget.value, at: event.currentTarget.selectionStart })}
                        onBlur={() => setHintsClosed(true)}
                        onPaste={(event) => {
                          const files = imageFilesFromClipboard(event.clipboardData)
                          if (files.length > 0) {
                            event.preventDefault()
                            void attachImages(files)
                          }
                        }}
                        onChange={(event) => {
                          setPrompt(event.currentTarget.value)
                          setCaret({ draft: event.currentTarget.value, at: event.currentTarget.selectionStart })
                          setHintIndex(0)
                          setHintsClosed(false)
                        }}
                        onKeyDown={(event) => {
                          if (event.nativeEvent.isComposing || event.keyCode === 229) return
                          if (composerHints.length === 0) return
                          if (!event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey) {
                            const move = event.key === "ArrowDown" ? 1 : event.key === "ArrowUp" ? -1 : 0
                            if (move) {
                              event.preventDefault()
                              setHintIndex(moveHint(activeHint, move, composerHints.length))
                              return
                            }
                            if (event.key === "Tab" || event.key === "Enter") {
                              event.preventDefault()
                              pickHint(composerHints[activeHint]!)
                              return
                            }
                          }
                          if (event.key === "Escape") {
                            event.preventDefault()
                            event.stopPropagation()
                            setHintsClosed(true)
                          }
                        }}
                        placeholder={resuming ? "Message to send after resuming (optional)" : `Describe the task for ${harnessName(harnessId)} (optional)`}
                        className="relative block field-sizing-content max-h-[30dvh] min-h-20 w-full resize-none bg-transparent px-(--s5) pt-(--s4) text-(length:--fs) outline-none placeholder:text-(--fg3)"
                      />
                    </div>
                  ) : (
                    <p className="px-(--s5) pt-(--s4) pb-(--s2) text-(length:--fs-sm) text-(--fg2)">{`Opens ${harnessName(harnessId)} in an interactive terminal. Nothing is sent.`}</p>
                  )}
                  {view === "chat" && images.length > 0 && (
                    <div className="flex max-h-[30dvh] min-w-0 flex-col gap-(--s2) overflow-y-auto px-(--s3) pt-(--s2)">
                      {images.map((image) => (
                        <AttachmentRow key={image.previewUrl} name={image.name} previewUrl={image.previewUrl} disabled={!mutable} onRemove={() => removeImage(image)} />
                      ))}
                    </div>
                  )}
                  {view === "chat" && imageError && <FieldError className="px-(--s5) pt-(--s2)">{imageError}</FieldError>}
                  {/* How it runs: macOS pop-up buttons, one per decision, with remembered defaults. */}
                  <div className="flex min-w-0 flex-wrap items-center gap-(--s2) px-(--s3) pt-(--s2) pb-(--s2)">
                    <PopupSelect label="Agent" value={harnessId} onValueChange={(id) => { setHarness(id); setResumeId("") }}
                      items={agentIds.map((id) => {
                        const used = row.launches.find((launch) => launch.harness === id)
                        const missing = accounts.data?.some((entry) => entry.harness === id && !entry.installed)
                        return { value: id, label: harnessName(id), disabled: missing, detail: missing ? "not installed" : used ? shortAge(new Date(used.last_used), now) : undefined }
                      })} />
                    {view === "chat" && modelOption && (
                      <PopupSelect
                        label="Model"
                        value={chosenModel ?? automaticChoice}
                        disabled={!mutable}
                        onValueChange={(value) => {
                          if (optionScope) setModelPicks((current) => ({ ...current, [optionScope]: value }))
                          start.reset()
                        }}
                        items={launchChoices(modelOption, "Model")}
                      />
                    )}
                    {view === "chat" && effortOption && (
                      <PopupSelect
                        label="Reasoning"
                        value={chosenEffort ?? automaticChoice}
                        disabled={!mutable}
                        onValueChange={(value) => {
                          if (optionScope) setEffortPicks((current) => ({ ...current, [optionScope]: value }))
                          start.reset()
                        }}
                        items={launchChoices(effortOption, "Reasoning")}
                      />
                    )}
                    {modes.length > 1 && (
                      <PopupSelect label="Mode" value={modeValue} onValueChange={(value) => { setMode(value); setResumeId("") }}
                        items={modes.map((name) => ({ value: name, label: name === "default" ? "Default mode" : name }))} />
                    )}
                    {showWorkspace && !resuming && (
                      <div ref={worktreeControl} aria-invalid={worktreeError ? true : undefined} aria-describedby={worktreeError ? worktreeErrorId : undefined} className="contents">
                        <PopupSelect label="Workspace" value={workspace} onValueChange={(value) => { setWorkspace(value); start.reset() }}
                          items={[
                            { value: "folder", label: "This folder" },
                            { value: "new", label: "New worktree", disabled: !canWorktree, detail: worktreesNeedUpdate ? "needs update" : undefined },
                            ...(canWorktree ? projectWorktrees.map((item) => ({ value: item.path, label: item.branch ?? item.path, group: "Continue a worktree", detail: plural(item.sessions.length, "session") })) : []),
                          ]} />
                      </div>
                    )}
                  </div>
                  {/* The composer toolbar: one line that never wraps. */}
                  <div className="flex items-center gap-(--s1) px-(--s3) pb-(--s3)">
                    {view === "chat" && (
                      <>
                        <input
                          ref={fileRef}
                          type="file"
                          accept="image/png,image/jpeg,image/gif,image/webp"
                          multiple
                          className="hidden"
                          onChange={(event) => {
                            const files = Array.from(event.currentTarget.files ?? [])
                            event.currentTarget.value = ""
                            void attachImages(files)
                          }}
                        />
                        <Button type="button" size="icon" variant="ghost" aria-label="Attach image" title="Attach images" disabled={reading} onClick={() => fileRef.current?.click()}>
                          <Plus aria-hidden />
                        </Button>
                      </>
                    )}
                    {view === "chat" && hasAgentCommands && (
                      <Button
                        type="button"
                        size="icon"
                        variant="ghost"
                        aria-label="Commands"
                        title="Insert a command (/)"
                        onMouseDown={(event) => event.preventDefault()}
                        onClick={openSlashHints}
                      >
                        <SquareSlash aria-hidden />
                      </Button>
                    )}
                    <SegmentedControl value={[view]} onValueChange={chooseView} aria-label="Open as" className="ms-auto">
                      <SegmentedControlItem value="chat" disabled={Boolean(chatUnavailableHint)} title={chatUnavailableHint}>Chat</SegmentedControlItem>
                      <SegmentedControlItem value="terminal">Terminal</SegmentedControlItem>
                    </SegmentedControl>
                  </div>
                  <ComposerHintsMenu
                    id={hintsId}
                    placement="below"
                    activeIndex={activeHint}
                    title={hintTitle}
                    hints={composerHints}
                    onPick={pickHint}
                    query={slashQuery(beforeCaret)?.query ?? ""}
                    agentName={harnessName(harnessId)}
                  />
                </div>

                {/* One quiet line per thing worth knowing, each with its own action. */}
                <div className="flex min-w-0 flex-col gap-(--s2) px-(--s2) text-(length:--fs-sm) text-(--fg2) empty:hidden">
                  {view === "chat" && !resuming && (pastSessions.data?.length ?? 0) > 0 && (
                    <button type="button" onClick={() => setResume(true)} className="flex items-center gap-(--s2) self-start text-(--accent) hover:underline">
                      <History aria-hidden className="size-3.5" />
                      {`Resume a past ${harnessName(harnessId)} conversation (${pastSessions.data!.length})`}
                    </button>
                  )}
                  {showModelAbsenceCue && (
                    <p>{`Model options aren't saved for ${harnessName(harnessId)} on ${machineName} yet. You can choose a model in chat before sending.`}</p>
                  )}
                  {workspace === "new" && (
                    <p className="flex flex-wrap items-center gap-x-(--s3) gap-y-(--s1)">
                      <span>{view === "chat" && prompt.trim() ? `New branch ${branchName} from ${row.git?.branch || "the current commit"}.` : view === "chat" ? `New branch from ${row.git?.branch || "the current commit"}, named after the first message.` : `New branch from ${row.git?.branch || "the current commit"}.`}</span>
                      {dirty > 0 && (
                        <label className="flex items-center gap-(--s2)">
                          <input type="checkbox" checked={carry} onChange={(event) => setCarry(event.currentTarget.checked)} className="accent-(--accent)" />
                          {`Bring ${plural(dirty, "uncommitted change")}`}
                        </label>
                      )}
                    </p>
                  )}
                  {continued && <p>{`Shares ${continued.branch} with ${plural(continued.sessions.length, "other session")}.`}</p>}
                  {workspace === "folder" && othersHere.length > 0 && (
                    <p className="flex flex-wrap items-center gap-x-(--s3)">
                      <span>{`${plural(othersHere.length, "other agent")} ${othersHere.length === 1 ? "is" : "are"} working in this folder.`}</span>
                      {canWorktree && <button type="button" onClick={() => { setWorkspace("new"); start.reset() }} className="text-(--accent) hover:underline">Use a new worktree</button>}
                    </p>
                  )}
                  {account?.installed && !account.signedIn && !account.checkRequired && (
                    <p className="flex flex-wrap items-center gap-x-(--s3)">
                      <span>{`${harnessName(harnessId)} isn't signed in on ${machineName}.`}{view === "chat" && !resuming && " You can also sign in from the chat."}</span>
                      {account.methods.length > 0
                        ? <button type="button" onClick={() => setSignInTarget({ machine: place.machine, name: machineName, account })} className="text-(--accent) hover:underline">Sign in…</button>
                        : <span>{`Run ${harnessName(harnessId)} on ${machineName} to sign in.`}</span>}
                    </p>
                  )}
                  {worktreeError && <FieldError id={worktreeErrorId}>{worktreeError}</FieldError>}
                </div>
              </div>

              {resuming && (
                <section aria-label="Conversations" className="flex min-w-0 flex-col gap-(--s2)">
                  <GroupHeader className="px-(--s2)">{`${harnessName(harnessId)} conversations in ${project.name}`}</GroupHeader>
                  {pastSessions.isLoading ? (
                    <span role="status" className="px-(--s2) text-(--fg2)">Loading conversations…</span>
                  ) : pastSessions.data?.length ? (
                    <SourceList aria-label="Past conversations" className="max-h-56 overflow-y-auto">
                      {pastSessions.data.map((item) => (
                        <SourceRow key={item.id} type="button" selected={resumeId === item.id} onClick={() => setResumeId(item.id)}>
                          <span className="min-w-0 flex-1 truncate">{item.title?.trim() || item.id}</span>
                        </SourceRow>
                      ))}
                    </SourceList>
                  ) : (
                    <p className="px-(--s2) text-(--fg2)">{signInError(pastSessions.error) ? "Sign in to list past conversations." : pastSessions.isError ? "Past conversations are unavailable." : "No conversations to resume for this project and agent."}</p>
                  )}
                </section>
              )}

              {contentsLoading ? (
                <p className="text-(length:--fs-sm) text-(--fg2)">{`Loading what this folder loads on ${machineName}…`}</p>
              ) : row?.details_deferred ? null : (
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
              : start.needsImagesUpdate
                ? `Needs update · Install a newer Agentpack on ${machineName} to attach images, or remove them.`
                : start.error.message}</FieldError>}
            <SheetActions pinned={false}>
              {!showProjectPicker && (
                <>
                  {resuming && hasMessage && (
                    <Button type="button" disabled={blocked || images.length > 0} title={images.length > 0 ? "Remove images to resume without sending" : undefined}
                      className="disabled:pointer-events-auto" onClick={() => launch()}>Resume only</Button>
                  )}
                  <Button type="submit" variant="primary" disabled={blocked}>
                    {resuming ? hasMessage ? "Resume and send" : "Resume" : view === "terminal" ? "Open terminal" : hasMessage ? "Start and send" : "Start chat"}
                    <Kbd aria-hidden className="text-current opacity-70">{isMac ? "⌘↵" : "Ctrl+↵"}</Kbd>
                  </Button>
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
    // Folded by default: it's reference, not a decision. Opens itself when a login must be picked or the lock is broken.
    <details aria-label="Loaded in this session" open={choosable || Boolean(project.error) || undefined} className="group/loads flex min-w-0 flex-col gap-(--s3) open:[&>summary]:mb-(--s3)">
      <summary className="flex cursor-default list-none items-center gap-(--s2) rounded-(--r-xs) text-(length:--fs-sm) text-(--fg2) hover:text-(--fg) [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-3.5 transition-transform duration-(--dur-1) ease-(--ease) group-open/loads:rotate-90" />
        {project.packages.length + project.mcp.length === 0 ? "Loads only the agent's own skills" : `Loads ${[project.packages.length && plural(project.packages.length, "package"), project.mcp.length && plural(project.mcp.length, "MCP server")].filter(Boolean).join(" and ")}`}
        <span className="text-(--fg3)">{` · ${active} active${off > 0 ? `, ${off} off in ${mode}` : ""}`}</span>
      </summary>
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
    </details>
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
