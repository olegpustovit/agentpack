import { createFileRoute, Outlet, useNavigate, useParams } from "@tanstack/react-router"
import { lazy, Suspense, useRef, useState } from "react"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { SessionsHome, type WorkPosition } from "@/features/sessions/home"
import { NewSessionLink, SessionList } from "@/features/sessions/session-list"
import { ListDetail } from "@/features/shell/list-detail"
import { ToolbarSlot } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { looseSearch } from "@/lib/search"

const NewSessionSheet = lazy(() => import("@/features/sessions/new-session-sheet").then((module) => ({ default: module.NewSessionSheet })))

function SessionsLayout() {
  const search = Route.useSearch()
  const { sessionId, folder } = useParams({ strict: false })
  const navigate = useNavigate()
  const isPhone = useIsPhone()
  const [draft, setDraft] = useState("")
  const restore = useRef<WorkPosition | null>(null)
  const all = search.tab === "all" || (search.tab !== "work" && Boolean(search.q || search.group || search.agent || search.status || search.host))
  // History is the Work page's other view; its search lives in the URL so Back and links keep it.
  const history = {
    search: { find: search.find, project: search.project, on: search.on, by: search.by },
    setSearch: (patch: Record<string, string | undefined>) => void navigate({ to: ".", search: (prev) => ({ ...prev, ...patch }), replace: true }),
  }
  const work = <SessionsHome draft={draft} setDraft={setDraft} view={search.view === "history" ? "history" : "work"} history={history}
    onView={(view) => void navigate({ to: "/sessions", search: (prev) => ({ ...prev, view: view === "history" ? "history" : undefined }) })}
    rememberPosition={(position) => { restore.current = position }}
    takePosition={() => {
      const position = restore.current
      restore.current = null
      return position
    }}
    onReview={() => void navigate({ to: "/sessions", search: (prev) => ({ ...prev, new: "1" }), replace: true })} />
  const list = isPhone && !sessionId ? (
    <>
      {!all && <ToolbarSlot><NewSessionLink /></ToolbarSlot>}
      <SegmentedControl aria-label="Sessions view" className="mx-(--s4) my-(--s3) shrink-0 self-start"
        value={[all ? "all" : "work"]} onValueChange={([tab]) => {
          if (tab === "work" || tab === "all") void navigate({ to: "/sessions", search: (prev) => ({ ...prev, tab }) })
        }}>
        <SegmentedControlItem value="work">Work</SegmentedControlItem>
        <SegmentedControlItem value="all">All sessions</SegmentedControlItem>
      </SegmentedControl>
      {all ? <div className="min-h-0 flex-1"><SessionList /></div> : work}
    </>
  ) : <SessionList />

  return (
    <>
      <ListDetail list={list}>
        {isPhone || sessionId || folder ? <Outlet /> : work}
      </ListDetail>
      {search.new === "1" && (
        <Suspense fallback={null}>
          <NewSessionSheet open initialMachine={search.machine} initialDirectory={search.folder} initialPrompt={draft}
            onAccepted={(input) => setDraft(input.agentSessionId && !input.sendOnResume ? input.prompt ?? "" : "")}
            onOpenChange={(open) => {
              if (!open) void navigate({ to: ".", search: (prev) => ({ ...prev, new: undefined, machine: undefined, folder: undefined }), replace: true })
            }} />
        </Suspense>
      )}
    </>
  )
}

export const Route = createFileRoute("/_app/sessions")({
  // machine and folder preselect where New session starts. Child routes add their own keys.
  validateSearch: looseSearch({
    q: "text",
    group: "text",
    agent: "text",
    status: "text",
    host: "text",
    tab: "text",
    new: "flag",
    machine: "text",
    folder: "text",
    // The Work page's view and History's search: find (text), project (folder name), on (machine id), by (agent).
    view: "text",
    find: "text",
    project: "text",
    on: "text",
    by: "text",
  }),
  component: SessionsLayout,
})
