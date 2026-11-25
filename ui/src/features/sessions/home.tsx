import { useEffect, useLayoutEffect, useRef, useState, type MouseEvent, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Check, CornerUpLeft } from "lucide-react"
import { AgentTile } from "@/components/kit/agent-tile"
import { Empty, Kbd } from "@/components/kit/feedback"
import { GroupHeader } from "@/components/kit/list"
import { Button, buttonVariants } from "@/components/ui/button"
import { conversationEventsQuery } from "@/features/conversation/queries"
import { isOpenRequest, reduceThread } from "@/features/conversation/reducer"
import { ElicitationCard, PermissionCard } from "@/features/conversation/request-cards"
import { basename, harnessName, shortAge } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { lastSession } from "@/features/shell/views"
import { lastActivity, rowKey, sessionLabel, type ListedSession } from "./group"
import { useListedSessions } from "./queries"
import { isUnread, markSeen, openWaits, sessionGlyphState, useSeen, waitingDetail } from "./session-status"
import { projectDirectory, sessionGitQuery } from "./git"

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

// The Sessions pane with nothing open: start a task, answer what waits on you, see what runs and what finished.
export type WorkPosition = { top: number; group: string }

export function SessionsHome({ draft, setDraft, onReview, rememberPosition, takePosition }: {
  draft: string
  setDraft: (draft: string) => void
  onReview: () => void
  rememberPosition: (position: WorkPosition) => void
  takePosition: () => WorkPosition | null
}) {
  const { sessions, queries } = useListedSessions()
  const seen = useSeen()
  const scroller = useRef<HTMLDivElement>(null)
  const last = lastSession()
  useLayoutEffect(() => {
    if (location.pathname !== "/sessions" && location.pathname !== "/sessions/") return
    const view = scroller.current
    const previous = takePosition()
    if (!previous || !view) return
    view.scrollTop = previous.top
    const heading = view.querySelector<HTMLElement>(`[data-work-group="${previous.group}"] h2`)
    heading?.focus({ preventScroll: true })
    heading?.scrollIntoView({ block: "nearest" })
  }, [])

  function onOpenWorkItem(event: MouseEvent<HTMLDivElement>) {
    if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || !(event.target instanceof Element)) return
    const group = event.target.closest("a[href]")?.closest<HTMLElement>("[data-work-group]")?.dataset.workGroup
    if (group) rememberPosition({ group, top: event.currentTarget.scrollTop })
  }
  const now = useNow(true, 60_000)
  const needsYou = sessions.filter((session) => sessionGlyphState(session) === "needsYou")
  const working = sessions.filter((session) => sessionGlyphState(session) === "running" && session.conversation_state === "working")
  const waiting = sessions.filter((session) => sessionGlyphState(session) === "waiting")
  const ready = sessions.filter((session) => isUnread(session, seen[rowKey(session)] ?? seen[""]))
    .sort((a, b) => (lastActivity(b) ?? "").localeCompare(lastActivity(a) ?? ""))
  const quiet = !queries.firstLoadPending && needsYou.length + working.length + waiting.length + ready.length === 0
  const chatName = (id: string) => {
    const chat = sessions.find((session) => session.id === id)
    return chat && sessionLabel(chat)
  }
  const summary = [
    needsYou.length && `${needsYou.length} need${needsYou.length === 1 ? "s" : ""} you`,
    ready.length && `${ready.length} to review`,
    working.length && `${working.length} working`,
    waiting.length && `${waiting.length} waiting`,
  ].filter(Boolean).join(" · ")
  const lastChat = last && sessions.find((session) => session.machine === last.machine && session.id === last.sessionId)

  return (
    <div ref={scroller} className="h-full min-h-0 flex-1 overflow-y-auto" onClickCapture={onOpenWorkItem}>
      <div className="mx-auto flex max-w-[52rem] flex-col gap-(--s8) px-(--s8) py-(--s8) max-[760px]:px-(--s5) max-[760px]:py-(--s6)">
        <header className="flex flex-col gap-(--s1)">
          <h2 className="text-(length:--fs-lg) font-semibold">Work</h2>
          {!queries.firstLoadPending && <p className="text-(length:--fs-sm) text-(--fg2)">{summary || "Nothing needs attention"}</p>}
        </header>

        {/* Composer first: the task is the start; where it runs is chosen on the next step. */}
        <form aria-label="Start a task" onSubmit={(event) => {
          event.preventDefault()
          onReview()
        }} className="flex flex-col rounded-(--r-pop) bg-(--field) shadow-[inset_0_0_0_.5px_var(--control-edge)] transition-shadow duration-(--dur-1) ease-(--ease) focus-within:shadow-[inset_0_0_0_1px_var(--accent),0_0_0_3px_var(--accent-tint)]">
          <textarea aria-label="Task" placeholder="Describe a task…" rows={2} value={draft}
            onChange={(event) => setDraft(event.currentTarget.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                event.preventDefault()
                event.currentTarget.form?.requestSubmit()
              }
            }}
            className="field-sizing-content max-h-60 min-h-14 resize-none bg-transparent px-(--s5) pt-(--s4) text-(length:--fs) outline-none placeholder:text-(--fg3)" />
          <div className="flex items-center gap-(--s3) px-(--s3) pb-(--s3)">
            {last && (
              <Link to="/sessions/$machine/$sessionId" params={{ machine: last.machine, sessionId: last.sessionId }} search={last.search}
                className={buttonVariants({ variant: "ghost", size: "sm", className: "min-w-0 text-(--fg2)" })}>
                <CornerUpLeft aria-hidden className="size-3.5" />
                <span className="truncate">{lastChat ? `Continue “${sessionLabel(lastChat)}”` : "Continue last session"}</span>
              </Link>
            )}
            <Kbd className="ms-auto font-(family-name:--font)">{isMac ? "⌘↵" : "Ctrl+↵"}</Kbd>
            <Button type="submit" variant="primary" size="sm">Start a task</Button>
          </div>
        </form>

        {needsYou.length > 0 && (
          <Section title="Needs you" count={needsYou.length} tone="warn" aside={<span className="text-(--fg3)">{`${isMac ? "⌘⇧J" : "Ctrl+Shift+J"} next`}</span>}>
            {needsYou.map((session) => <WaitingItem key={rowKey(session)} session={session} now={now} />)}
          </Section>
        )}
        {ready.length > 0 && (
          <Section title="Ready for review" count={ready.length} limit={5}
            aside={ready.length > 1 && <button type="button" onClick={() => ready.forEach((session) => markSeen(rowKey(session), lastActivity(session)))} className="font-normal text-(--accent) hover:underline">Mark all reviewed</button>}>
            {ready.map((session) => (
              <HomeRow key={rowKey(session)} session={session} now={now} state="done">
                <DiffStat session={session} />
                <RowAction label={`Mark “${sessionLabel(session)}” reviewed`} onClick={() => markSeen(rowKey(session), lastActivity(session))}>
                  <Check aria-hidden />
                </RowAction>
              </HomeRow>
            ))}
          </Section>
        )}
        {working.length > 0 && (
          <Section title="Working" count={working.length} limit={5}>
            {working.map((session) => <HomeRow key={rowKey(session)} session={session} now={now} />)}
          </Section>
        )}
        {waiting.length > 0 && (
          <Section title="Waiting on background work" count={waiting.length} limit={5}>
            {waiting.map((session) => <HomeRow key={rowKey(session)} session={session} now={now} reason={waitingDetail(openWaits(session, now), chatName, now)} />)}
          </Section>
        )}
        {quiet && <Empty title="All caught up">Nothing is waiting on you. Describe a task above, or open a session from the list.</Empty>}
      </div>
    </div>
  )
}

// A titled group. Long groups show their first rows and a "Show N more" toggle.
function Section({ title, count, tone, aside, limit, children }: { title: string; count: number; tone?: "warn"; aside?: ReactNode; limit?: number; children: ReactNode[] }) {
  const [expanded, setExpanded] = useState(false)
  const hidden = limit && !expanded ? Math.max(0, children.length - limit) : 0
  return (
    <section aria-label={title} data-work-group={title} className="flex flex-col gap-(--s2)">
      <GroupHeader className="h-auto min-h-(--head-h) px-0">
        <h2 tabIndex={-1} className="flex min-w-0 flex-1 items-center gap-(--s3) rounded-(--r-xs)">
          {tone === "warn" && <span aria-hidden className="size-2 rounded-full bg-(--warn)" />}
          {title}
          <span className="font-normal text-(--fg3) tabular-nums">{count}</span>
        </h2>
        {aside}
      </GroupHeader>
      <ul className="-mx-(--s3) flex flex-col">
        {hidden ? children.slice(0, limit) : children}
      </ul>
      {limit && children.length > limit && (
        <button type="button" onClick={() => setExpanded(!expanded)} className="self-start rounded-(--r-xs) text-(length:--fs-sm) text-(--accent) hover:underline">
          {expanded ? "Show less" : `Show ${hidden} more`}
        </button>
      )}
    </section>
  )
}

// Two lines, as in Mail and Linear lists: the title, then project · machine (or what it waits for). The whole row opens the session.
function HomeRow({ session, now, state, reason, children }: { session: ListedSession; now: number; state?: string; reason?: string; children?: ReactNode }) {
  const when = lastActivity(session)
  const label = sessionLabel(session)
  const meta = [basename(projectDirectory(session)), session.machineName].filter(Boolean).join(" · ")
  return (
    <li className="group/row relative grid min-h-(--row-h-2) grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-(--s4) rounded-(--r-row) px-(--s3) py-(--s2) transition-colors duration-(--dur-1) ease-(--ease) hover:bg-(--hover) has-focus-visible:bg-(--hover)">
      <AgentTile harness={session.harness} state={state ?? sessionGlyphState(session)} unread={state === "done"} label={harnessName(session.harness)} className="[--dot-ring:var(--page)]" />
      <div className="flex min-w-0 flex-col">
        <Link
          to="/sessions/$machine/$sessionId"
          params={{ machine: session.machine, sessionId: session.id }}
          search={true}
          title={label}
          className="truncate text-(length:--fs) text-(--fg) outline-none after:absolute after:inset-0 after:rounded-(--r-row) focus-visible:after:ring-2 focus-visible:after:ring-(--accent)"
        >
          {label}
        </Link>
        <span className="truncate text-(length:--fs-sm) text-(--fg2)" title={reason || undefined}>{reason ? `${meta} · ${reason}` : meta}</span>
      </div>
      <div className="relative flex items-center gap-(--s4)">
        {children}
        {when && <time dateTime={when} className="min-w-8 text-right text-(length:--fs-sm) text-(--fg3) tabular-nums">{shortAge(new Date(when), now)}</time>}
      </div>
    </li>
  )
}

// A quiet row action, shown on hover or focus (always on touch); it sits above the row's link.
function RowAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <Button type="button" variant="ghost" size="icon" aria-label={label} title={label} onClick={onClick}
      className="relative z-1 -my-(--s2) opacity-0 transition-opacity duration-(--dur-1) group-hover/row:opacity-100 focus-visible:opacity-100 pointer-coarse:opacity-100">
      {children}
    </Button>
  )
}

// Lines changed since the session's branch left its base, read on the machine running it. A peer
// without the route (404) or a folder outside a repository shows nothing.
function DiffStat({ session }: { session: ListedSession }) {
  const { data } = useQuery({ ...sessionGitQuery(session.machine, session.id), enabled: !session.offline })
  if (!data?.available || data.added + data.deleted === 0) return null
  return (
    <span className="shrink-0 text-(length:--fs-xs) tabular-nums" aria-label={`${data.added} lines added, ${data.deleted} removed`}>
      <span className="text-(--ok-word)">+{data.added}</span> <span className="text-(--danger-word)">−{data.deleted}</span>
    </span>
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
  // Its own key: the open chat's pane folds a longer log for the same session.
  const items = reduceThread(`waiting\0${session.machine}\0${session.id}`, data?.entries ?? []).items
  const pending = items.findLast(isOpenRequest)
  return (
    <>
      <HomeRow session={session} now={now} />
      {pending && (
        <li className="px-(--s3) pb-(--s3) ps-[calc(var(--s3)+18px+var(--s4))]">
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
