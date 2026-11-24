import { useEffect, useLayoutEffect, useRef, type MouseEvent, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Empty } from "@/components/kit/feedback"
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
import { isUnread, openWaits, sessionGlyphState, useSeen, waitingDetail } from "./session-status"
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
  const quiet = !queries.firstLoadPending && needsYou.length + working.length + waiting.length + ready.length === 0
  const chatName = (id: string) => {
    const chat = sessions.find((session) => session.id === id)
    return chat && sessionLabel(chat)
  }

  return (
    <div ref={scroller} className="h-full min-h-0 flex-1 overflow-y-auto" onClickCapture={onOpenWorkItem}>
      <div className="mx-auto flex max-w-[46rem] flex-col gap-(--s6) px-(--s6) py-(--s6)">
        <div className="flex flex-wrap items-center justify-between gap-(--s3)">
          <h2 className="text-(length:--fs-lg) font-semibold">Work</h2>
          {last && (
            <Link to="/sessions/$machine/$sessionId" params={{ machine: last.machine, sessionId: last.sessionId }} search={last.search}
              className={buttonVariants({ size: "sm" })}>
              Continue last session
            </Link>
          )}
        </div>
        <form aria-label="Start a task" className="flex flex-wrap items-center gap-(--s3)" onSubmit={(event) => {
          event.preventDefault()
          onReview()
        }}>
          <textarea aria-label="Task" placeholder="Describe a task" rows={1} value={draft}
            onChange={(event) => setDraft(event.currentTarget.value)}
            className="min-h-(--ctl-h) min-w-40 flex-1 resize-y rounded-(--r-ctl) border-[0.5px] border-(--control-edge) bg-(--field) px-(--s3) py-(--s2) text-(length:--fs)" />
          <Button type="submit" variant="primary">Start a task</Button>
        </form>
        <Section title="Needs you" count={needsYou.length} hint={needsYou.length > 0 ? `${isMac ? "⌘⇧J" : "Ctrl+Shift+J"} jumps to the next one` : undefined}>
          {needsYou.map((session) => <WaitingItem key={rowKey(session)} session={session} now={now} />)}
        </Section>
        <Section title="Working" count={working.length}>
          {working.map((session) => <HomeRow key={rowKey(session)} session={session} now={now} />)}
        </Section>
        <Section title="Waiting on background work" count={waiting.length}>
          {waiting.map((session) => <HomeRow key={rowKey(session)} session={session} now={now} reason={waitingDetail(openWaits(session, now), chatName, now)} />)}
        </Section>
        <Section title="Ready for review" count={ready.length}>
          {ready.map((session) => (
            <HomeRow key={rowKey(session)} session={session} now={now} unread>
              <DiffStat session={session} />
              <Link to="/sessions/$machine/$sessionId" params={{ machine: session.machine, sessionId: session.id }} search={true} className={buttonVariants({ size: "sm" })}>
                Review<span className="sr-only"> {sessionLabel(session)}</span>
              </Link>
            </HomeRow>
          ))}
        </Section>
        {quiet && <Empty title="Nothing waiting">Start a task above or choose a session from the list.</Empty>}
      </div>
    </div>
  )
}

function Section({ title, count, hint, children }: { title: string; count: number; hint?: string; children: ReactNode }) {
  return (
    <section aria-label={title} data-work-group={title} className="flex flex-col">
      <GroupHeader className="h-auto min-h-(--head-h) flex-wrap border-b-[0.5px] border-(--sep) px-0">
        <h2 tabIndex={-1} className="min-w-0 flex-1 rounded-(--r-xs)">{title} · {count}</h2>
        {hint && <span className="font-normal text-(--fg3)">{hint}</span>}
      </GroupHeader>
      {count > 0 && <ul className="flex flex-col">{children}</ul>}
    </section>
  )
}

function HomeRow({ session, now, unread, reason, children }: { session: ListedSession; now: number; unread?: boolean; reason?: string; children?: ReactNode }) {
  const when = lastActivity(session)
  return (
    <li className="flex min-h-(--row-h) min-w-0 flex-wrap items-center gap-x-(--s4) gap-y-(--s1) border-b-[0.5px] border-(--sep-soft) py-(--s2)">
      <Link
        to="/sessions/$machine/$sessionId"
        params={{ machine: session.machine, sessionId: session.id }}
        search={true}
        className="min-w-0 flex-1 wrap-anywhere text-(length:--fs) text-(--fg) hover:underline data-[unread=true]:font-semibold"
        data-unread={unread ? "true" : undefined}
      >
        {sessionLabel(session)}
      </Link>
      <span className="min-w-0 wrap-anywhere text-(length:--fs-sm) text-(--fg2) max-[760px]:order-last max-[760px]:basis-full">
        {[basename(projectDirectory(session)), session.machineName, reason || (when && `active ${shortAge(new Date(when), now)}`)].filter(Boolean).join(" · ")}
      </span>
      {children}
    </li>
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
