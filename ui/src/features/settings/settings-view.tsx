import { Suspense, type ComponentType } from "react"
import { useQuery } from "@tanstack/react-query"
import { Monitor, RefreshCw, Server, Users } from "lucide-react"
import { Empty, RowSkeleton } from "@/components/kit/feedback"
import { identityQuery } from "@/features/machines/queries"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { cn } from "@/lib/utils"
import { AccountPane } from "./account-pane"
import { NotificationsPane } from "./notifications-pane"
import { AgentsPane, AppearancePane, ConnectionsPane, DiagnosticsPane, GeneralPane, OmpPane, SharingPane } from "./panes"
import { ReplicationPane } from "./replication-pane"
import { sections, type Scope } from "./sections"

const views: Record<string, ComponentType> = {
  account: AccountPane, general: GeneralPane, sharing: SharingPane, diagnostics: DiagnosticsPane, replication: ReplicationPane,
  connections: ConnectionsPane, agents: AgentsPane, omp: OmpPane, notifications: NotificationsPane, appearance: AppearancePane,
}

function scopeLine(scope: Scope, machine: string) {
  if (scope === "machine") return { icon: Server, text: `Applies to ${machine || "this machine"}` }
  if (scope === "mesh") return { icon: RefreshCw, text: "Shared with every enrolled machine" }
  if (scope === "browser") return { icon: Monitor, text: "Saved in this browser only" }
  return { icon: Users, text: "Your gateway account on every machine" }
}

// One pane. The list beside it is the System sidebar.
export function SettingsView({ pane }: { pane: string }) {
  const current = sections.find((item) => item.id === pane)
  const View = views[pane]
  const machine = useQuery(identityQuery).data?.node.name ?? ""
  const isPhone = useIsPhone()
  const scope = current && scopeLine(current.scope, machine)
  return (
    <>
      <ToolbarTitle title={isPhone && current ? current.label : "Settings"} subtitle={machine} />
      <div className="h-full min-h-0 min-w-0 overflow-y-auto max-[760px]:scroll-pb-[calc(var(--tab-h)+var(--safe-b))]">
        <div className={cn("mx-auto flex w-full flex-col gap-(--s7) px-(--s6) pt-(--s7) pb-(--s9) max-[760px]:px-(--s5) max-[760px]:pt-(--s5)", current?.wide ? "max-w-270" : "max-w-170")}>
          {current && scope && View ? (
            <>
              <header className="flex flex-col gap-(--s2)">
                <h2 className="text-(length:--t-large) font-semibold max-[760px]:sr-only">{current.label}</h2>
                <p className="flex items-center gap-(--s2) text-(length:--fs-sm) text-(--fg2)">
                  <scope.icon aria-hidden className="size-(--fs-sm)" />
                  {scope.text}
                </p>
                {current.about && <p className="max-w-[72ch] text-(length:--fs-sm) text-(--fg2)">{current.about}</p>}
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
