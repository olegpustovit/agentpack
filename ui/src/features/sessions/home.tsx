import { useEffect, useRef, useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Empty } from "@/components/kit/feedback"
import { FieldError, PopupSelect } from "@/components/kit/forms"
import { GroupHeader } from "@/components/kit/list"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { conversationEventsQuery } from "@/features/conversation/queries"
import { reduceLog } from "@/features/conversation/reducer"
import { ElicitationCard, PermissionCard } from "@/features/conversation/thread-view"
import { useMachineQueries } from "@/features/machines/queries"
import type { ProjectRow } from "@/lib/api-types"
import { basename, harnessName, shortAge } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { loadStartChoices } from "./choices"
import { lastActivity, rowKey, sessionLabel, type ListedSession } from "./group"
import { projectsQuery, useListedSessions } from "./queries"
import { agentIds, canStart, defaultAgent, mergeProjects, placeLabel } from "./rules"
import { isUnread, loadSeen, sessionGlyphState } from "./session-status"
import { useStartSession } from "./start"

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

// The Sessions pane with nothing open: start a task, answer what waits on you, see what runs and what finished.
export function SessionsHome() {
  const { sessions, queries } = useListedSessions()
  // Read once: opening a row from here marks it seen in the sidebar, which this pane then outlives.
  const [seen] = useState(loadSeen)
  const now = useNow(true, 60_000)
  const needsYou = sessions.filter((session) => sessionGlyphState(session) === "needsYou")
  const working = sessions.filter((session) => sessionGlyphState(session) === "running" && session.conversation_state === "working")
  const ready = sessions.filter((session) => isUnread(session, seen[rowKey(session)] ?? seen[""]))
  const quiet = !queries.firstLoadPending && needsYou.length + working.length + ready.length === 0

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-[46rem] flex-col gap-(--s6) px-(--s6) py-(--s6)">
        <TaskBar />
        {needsYou.length > 0 && (
          <Section title={`Needs you · ${needsYou.length}`} hint={`${isMac ? "⌘J" : "Ctrl+J"} jumps to the next one`}>
            {needsYou.map((session) => <WaitingItem key={rowKey(session)} session={session} now={now} />)}
          </Section>
        )}
        {working.length > 0 && (
          <Section title={`Working · ${working.length}`}>
            {/* shortcut: no live activity line (the list has no current-step field); add it when sessions carry one. */}
            {working.map((session) => <HomeRow key={rowKey(session)} session={session} now={now} />)}
          </Section>
        )}
        {ready.length > 0 && (
          <Section title={`Ready for review · ${ready.length}`}>
            {/* shortcut: no +/− per session; comes with the git work (M7). */}
            {ready.map((session) => (
              <HomeRow key={rowKey(session)} session={session} now={now} unread>
                <Link to="/sessions/$machine/$sessionId" params={{ machine: session.machine, sessionId: session.id }} search={true} className={buttonVariants({ size: "sm" })}>
                  Review
                </Link>
              </HomeRow>
            ))}
          </Section>
        )}
        {quiet && <Empty title="Nothing waiting">Start a task above, or pick a session on the left.</Empty>}
      </div>
    </div>
  )
}

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section aria-label={title.split(" · ")[0]} className="flex flex-col">
      <GroupHeader className="border-b-[0.5px] border-(--sep) px-0">
        <span className="min-w-0 flex-1 truncate">{title}</span>
        {hint && <span className="shrink-0 font-normal text-(--fg3)">{hint}</span>}
      </GroupHeader>
      <ul className="flex flex-col">{children}</ul>
    </section>
  )
}

function HomeRow({ session, now, unread, children }: { session: ListedSession; now: number; unread?: boolean; children?: ReactNode }) {
  const when = lastActivity(session)
  return (
    <li className="flex min-h-(--row-h) min-w-0 items-center gap-(--s4) border-b-[0.5px] border-(--sep-soft) py-(--s2)">
      <Link
        to="/sessions/$machine/$sessionId"
        params={{ machine: session.machine, sessionId: session.id }}
        search={true}
        className="min-w-0 flex-1 truncate text-(length:--fs) text-(--fg) hover:underline data-[unread=true]:font-semibold"
        data-unread={unread ? "true" : undefined}
      >
        {sessionLabel(session)}
      </Link>
      <span className="shrink-0 text-(length:--fs-sm) text-(--fg2)">
        {[basename(session.directory), session.machineName, when && `active ${shortAge(new Date(when), now)}`].filter(Boolean).join(" · ")}
      </span>
      {children}
    </li>
  )
}

// The request a waiting chat is blocked on, answerable here through the cards the thread uses.
function WaitingItem({ session, now }: { session: ListedSession; now: number }) {
  const activity = lastActivity(session)
  const { data, refetch } = useQuery({ ...conversationEventsQuery(session.machine, session.id), staleTime: 0, retry: false })
  // A new request on a chat that already waits arrives as activity, not as a state change.
  const fetchedFor = useRef(activity)
  useEffect(() => {
    if (fetchedFor.current === activity) return
    fetchedFor.current = activity
    void refetch()
  }, [activity, refetch])
  const items = reduceLog(data?.entries ?? []).items
  const pending = items.findLast((item) => {
    const ask = item.data as { requestId?: string; elicitationId?: string; resolved?: boolean; inactive?: boolean } | undefined
    if (ask?.resolved || ask?.inactive) return false
    return item.kind === "permission" ? Boolean(ask?.requestId) : item.kind === "elicitation" && Boolean(ask?.elicitationId)
  })
  return (
    <>
      <HomeRow session={session} now={now} />
      {pending && (
        <li className="border-b-[0.5px] border-(--sep-soft) py-(--s3)">
          {pending.kind === "permission" ? (
            <PermissionCard item={pending} machine={session.machine} sessionId={session.id} agent={harnessName(session.harness)} />
          ) : (
            <ElicitationCard item={pending} machine={session.machine} sessionId={session.id} />
          )}
        </li>
      )}
    </>
  )
}

// Project, machine and agent pickers over the projects query, defaulting to the last choices; Start opens the new session.
// The task text is the chat's first message, sent once the agent is up, as from New session.
function TaskBar() {
  const [task, setTask] = useState("")
  const { machines } = useListedSessions()
  const projectQueries = useMachineQueries(projectsQuery)
  const start = useStartSession()
  const [projectKey, setProjectKey] = useState<string | null>(null)
  const [placeKey, setPlaceKey] = useState<string | null>(null)
  const [harness, setHarness] = useState<string | null>(null)

  const rows: Record<string, ProjectRow[] | undefined> = {}
  for (const machine of machines) rows[machine.id] = projectQueries.byMachine[machine.id]?.data
  const projects = mergeProjects(machines, rows)
  const project = projects.find((item) => item.key === projectKey) ?? projects.find((item) => item.places.some(canStart))
  const place = project?.places.find((item) => item.key === placeKey) ?? project?.places.find(canStart)
  const remembered = place ? loadStartChoices(place.row.directory) : {}
  const usual = defaultAgent(remembered, place?.row.launches ?? [])
  const harnessId = harness ?? usual.harness
  const mode = harnessId === usual.harness ? usual.mode : place?.row.launches.find((launch) => launch.harness === harnessId)?.mode || "default"
  const view = remembered.view ?? "chat"
  const ok = Boolean(place && canStart(place))

  return (
    <form
      aria-label="Start a task"
      className="flex flex-col gap-(--s3)"
      onSubmit={(event) => {
        event.preventDefault()
        if (place && ok) start.mutate({ machine: place.machine, directory: place.row.directory, harness: harnessId, mode, view, prompt: task })
      }}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-(--s3) rounded-(--r-sheet) bg-(--window) p-(--s4) shadow-(--shadow-card)">
        {project && place ? (
          <>
            {view === "chat" && (
              <Input aria-label="Task" placeholder="Describe a task" value={task} onChange={(event) => setTask(event.currentTarget.value)} className="basis-full" />
            )}
            <PopupSelect
              label="Project"
              value={project.key}
              onValueChange={(next) => {
                setProjectKey(next)
                setPlaceKey(null)
                setHarness(null)
              }}
              items={projects.map((item) => ({ value: item.key, label: item.name, disabled: !item.places.some(canStart) }))}
            />
            <PopupSelect
              label="Machine"
              value={place.key}
              onValueChange={(next) => {
                setPlaceKey(next)
                setHarness(null)
              }}
              items={project.places.map((item) => ({ value: item.key, label: placeLabel(item, project.places), disabled: !canStart(item) }))}
            />
            <PopupSelect label="Agent" value={harnessId} onValueChange={setHarness} items={agentIds.map((id) => ({ value: id, label: harnessName(id) }))} />
          </>
        ) : (
          <span className="text-(--fg2)">{projectQueries.firstLoadPending ? "Loading projects…" : "Run an agent in a folder first, for example agentpack claude. It then appears here."}</span>
        )}
        <div className="flex-1" />
        <Button type="submit" variant="primary" disabled={!ok || start.isPending}>
          {start.isPending ? "Starting…" : "Start"}
        </Button>
      </div>
      {project && place && (
        <p className="px-(--s2) text-(length:--fs-sm) text-(--fg2)">
          Starts {harnessName(harnessId)} in <b className="font-semibold text-(--fg)">{project.name}</b> on <b className="font-semibold text-(--fg)">{place.machineName}</b>
          {mode !== "default" && ` · ${mode}`}
          {view === "terminal" && " · Terminal"}
          {view === "chat" && task.trim() && " · sends the task"}
        </p>
      )}
      {start.error && <FieldError>{start.error.message}</FieldError>}
    </form>
  )
}
