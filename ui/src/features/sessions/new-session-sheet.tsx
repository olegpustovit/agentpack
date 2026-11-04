import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Field, FieldError, FieldGrid, FieldLabel, PopupSelect } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { Machine } from "@/features/machines/queries"
import { api, ApiError } from "@/lib/api"
import type { AgentSession, SessionInfo } from "@/lib/api-types"
import { harnessName } from "@/lib/format"
import { sessionsQuery, projectsQuery } from "./queries"
import { startSessionBody, type SessionView } from "./rules"

// Harness ids the gateway starts.
const agents = ["claude", "codex", "grok", "omp", "opencode", "agent", "agy"].map((id) => ({
  value: id,
  label: harnessName(id),
}))
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

// Opened from ?new=1. The form mounts only while open, so each open starts from the defaults.
export function NewSessionSheet({
  open,
  machines,
  initialMachine,
  initialDirectory,
  onOpenChange,
}: {
  open: boolean
  machines: Machine[]
  initialMachine?: string
  initialDirectory?: string
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {open && (
        <SheetContent>
          <NewSessionForm machines={machines} initialMachine={initialMachine} initialDirectory={initialDirectory} />
        </SheetContent>
      )}
    </Sheet>
  )
}

function NewSessionForm({ machines, initialMachine, initialDirectory }: { machines: Machine[]; initialMachine?: string; initialDirectory?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const online = machines.filter((machine) => machine.online)
  // Nulls mean "use the default": the first online machine, its newest folder, and that folder's newest launch.
  const [machine, setMachine] = useState<string | null>(initialMachine ?? null)
  const [directory, setDirectory] = useState<string | null>(initialDirectory ?? null)
  const [harness, setHarness] = useState<string | null>(null)
  const [mode, setMode] = useState<string | null>(null)
  const [view, setView] = useState<SessionView>("chat")
  const [resumeId, setResumeId] = useState<string | null>(null)

  const machineId = machine ?? online[0]?.id ?? ""
  const folders = useQuery({ ...projectsQuery(machineId), enabled: machineId !== "" })
  const projects = (folders.data ?? []).filter((project) => project.available)
  const project = projects.find((item) => item.directory === directory) ?? projects[0]
  const newest = project?.launches[0]
  const harnessId = harness ?? newest?.harness ?? "claude"
  const modeValue = mode ?? (newest?.mode || "default")

  const pastSessions = useQuery({
    queryKey: ["agent-sessions", machineId, project?.directory, harnessId, modeValue],
    queryFn: async () => {
      const body = await api<{ sessions: AgentSession[] }>("/api/agent-sessions", {
        machine: machineId,
        query: { directory: project!.directory, harness: harnessId, mode: modeValue },
      })
      return body.sessions ?? []
    },
    enabled: view === "chat" && !!project && machineId !== "",
    retry: false,
  })

  const start = useMutation({
    mutationFn: (body: { directory: string; harness: string; mode: string; view: SessionView; agentSessionId?: string }) =>
      api<SessionInfo>("/api/sessions", { machine: machineId, method: "POST", body: startSessionBody(body) }),
    onSuccess: (info) => {
      void queryClient.invalidateQueries({ queryKey: sessionsQuery(machineId, false).queryKey })
      void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: machineId, sessionId: info.id } })
    },
  })

  function chooseMachine(next: string) {
    setMachine(next)
    setDirectory(null)
    setHarness(null)
    setMode(null)
  }

  function chooseProject(next: string) {
    setDirectory(next)
    setHarness(null)
    setMode(null)
  }

  function chooseView(value: unknown[]) {
    const next = value[0]
    if (next !== "chat" && next !== "terminal") return
    setView(next)
    if (next !== "chat") setResumeId(null)
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (project) start.mutate({ directory: project.directory, harness: harnessId, mode: modeValue, view, agentSessionId: resumeId ?? undefined })
  }

  // One reason at a time, most blocking first.
  const machineName = online.find((item) => item.id === machineId)?.name ?? "this machine"
  const chatUnavailableHint = chatUnavailableReason(pastSessions.error, machineName, harnessId)
  let hint: string | undefined
  if (online.length === 0) hint = "No machine is online."
  else if (folders.error instanceof ApiError && folders.error.status === 404) hint = `Update Agentpack on ${machineName} to see its projects.`
  else if (folders.error) hint = `Couldn't load projects from ${machineName}: ${folders.error.message}`
  else if (folders.isSuccess && projects.length === 0) hint = `Run an agent in a folder on ${machineName} first, for example agentpack claude. It then appears here.`
  else if (chatUnavailableHint) hint = chatUnavailableHint
  else if (view === "chat" && pastSessions.error) hint = `Couldn't load conversations: ${(pastSessions.error as Error).message}`

  return (
    <form onSubmit={submit} className="contents">
      <SheetTitle>New session</SheetTitle>
      <SheetDescription>Start an agent in a recent folder.</SheetDescription>
      <FieldGrid>
        <Field>
          <FieldLabel>Machine</FieldLabel>
          <PopupSelect
            label="Machine"
            value={machineId}
            onValueChange={chooseMachine}
            items={online.map((item) => ({ value: item.id, label: item.name }))}
          />
        </Field>
        <Field>
          <FieldLabel>Project</FieldLabel>
          {project ? (
            <PopupSelect
              label="Project"
              value={project.directory}
              onValueChange={chooseProject}
              items={projects.map((item) => ({ value: item.directory, label: item.directory }))}
            />
          ) : (
            <span className="min-w-0 text-(--fg2)">{folders.isPending ? "Loading projects…" : "No projects yet"}</span>
          )}
        </Field>
        <Field>
          <FieldLabel>Agent</FieldLabel>
          <PopupSelect label="Agent" value={harnessId} onValueChange={setHarness} items={agents} />
        </Field>
        <Field>
          <FieldLabel htmlFor="new-session-mode">Mode</FieldLabel>
          <Input id="new-session-mode" value={modeValue} onChange={(event) => setMode(event.target.value)} />
        </Field>
        <Field>
          <FieldLabel>View</FieldLabel>
          <SegmentedControl value={[view]} onValueChange={chooseView} aria-label="Session view" className="w-full">
            <SegmentedControlItem value="chat" className="flex-1" disabled={Boolean(chatUnavailableHint)}>
              Chat
            </SegmentedControlItem>
            <SegmentedControlItem value="terminal" className="flex-1">
              Terminal
            </SegmentedControlItem>
          </SegmentedControl>
        </Field>
        {view === "chat" && project && (
          <Field>
            <FieldLabel>Conversation</FieldLabel>
            {pastSessions.isPending ? (
              <span className="text-(--fg2)">Loading conversations…</span>
            ) : (pastSessions.data?.length ?? 0) === 0 ? (
              <span className="text-(--fg2)">New conversation</span>
            ) : (
              <PopupSelect
                label="Conversation"
                value={resumeId ?? ""}
                onValueChange={(value) => setResumeId(value === "" ? null : value)}
                items={[
                  { value: "", label: "New conversation" },
                  ...(pastSessions.data ?? []).map((item) => ({
                    value: item.id,
                    label: item.title?.trim() || item.id,
                  })),
                ]}
              />
            )}
          </Field>
        )}
      </FieldGrid>
      {hint && <p className="text-(length:--fs-sm) text-(--fg2)">{hint}</p>}
      {start.error && <FieldError>{start.error.message}</FieldError>}
      <SheetActions>
        <Button type="submit" variant="primary" disabled={!project || start.isPending || (view === "chat" && Boolean(chatUnavailableHint))}>
          {start.isPending ? "Starting session…" : "Start session"}
        </Button>
      </SheetActions>
    </form>
  )
}
