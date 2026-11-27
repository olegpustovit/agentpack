import { Suspense, type ComponentType } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ChevronLeft } from "lucide-react"
import { Empty, RowSkeleton } from "@/components/kit/feedback"
import { identityQuery } from "@/features/machines/queries"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { cn } from "@/lib/utils"
import { NotificationsPane } from "./notifications-pane"
import { AgentsPane, ConnectionsPane, DiagnosticsPane, GeneralPane, OmpPane, SharingPane } from "./panes"
import { sections } from "./sections"

const views: Record<string, ComponentType> = {
  general: GeneralPane, sharing: SharingPane, diagnostics: DiagnosticsPane,
  connections: ConnectionsPane, agents: AgentsPane, omp: OmpPane, notifications: NotificationsPane,
}

// One pane. The list beside it is the System sidebar; panes opened from General go back to it.
export function SettingsView({ pane }: { pane: string }) {
  const current = sections.find((item) => item.id === pane)
  const View = views[pane]
  const machine = useQuery(identityQuery).data?.node.name ?? ""
  const isPhone = useIsPhone()
  return (
    <>
      <ToolbarTitle title={isPhone && current ? current.label : "Settings"} subtitle={machine} />
      <div className="h-full min-h-0 min-w-0 overflow-y-auto max-[760px]:scroll-pb-[calc(var(--tab-h)+var(--safe-b))]">
        <div className={cn("mx-auto flex w-full flex-col gap-(--s7) px-(--s6) pt-(--s7) pb-(--s9) max-[760px]:px-(--s5) max-[760px]:pt-(--s5)", current?.wide ? "max-w-270" : "max-w-170")}>
          {current && View ? (
            <>
              <header className="flex items-center gap-(--s2) max-[760px]:sr-only">
                {current.parent && (
                  <Link to="/settings/$pane" params={{ pane: current.parent }} aria-label="Back to General" title="General" className="-ms-(--s3) grid size-(--ctl-h) shrink-0 place-items-center rounded-(--r-ctl) hover:bg-(--hover)">
                    <ChevronLeft aria-hidden className="size-4" />
                  </Link>
                )}
                <h2 className="text-(length:--t-large) font-semibold">{current.label}</h2>
              </header>
              <Suspense fallback={<RowSkeleton lines={2} />}><View /></Suspense>
            </>
          ) : (
            <Empty title="Settings section not found">Choose a section from the sidebar.</Empty>
          )}
        </div>
      </div>
    </>
  )
}
