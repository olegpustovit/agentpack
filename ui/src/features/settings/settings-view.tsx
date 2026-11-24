import { Suspense } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Empty, RowSkeleton } from "@/components/kit/feedback"
import { identityQuery } from "@/features/machines/queries"
import { ListDetail } from "@/features/shell/list-detail"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { SourceList, SourceRow } from "@/components/kit/list"
import { cn } from "@/lib/utils"
import { AccountPane } from "./account-pane"
import { NotificationsPane } from "./notifications-pane"
import { AccessibilityPane, AppearancePane, ConnectionsPane, DiagnosticsPane, GeneralPane, OmpPane, SharingPane } from "./panes"
import { ReplicationPane } from "./replication-pane"

const sections = [
  { id: "general", label: "General", view: GeneralPane },
  { id: "notifications", label: "Notifications", view: NotificationsPane },
  { id: "sharing", label: "Sharing", view: SharingPane },
  { id: "replication", label: "Replication", view: ReplicationPane },
  { id: "connections", label: "Connections", view: ConnectionsPane },
  // Role tables need more than the 640 px form width.
  { id: "omp", label: "Oh My Pi", view: OmpPane, wide: true },
  { id: "diagnostics", label: "Diagnostics", view: DiagnosticsPane },
  { id: "appearance", label: "Appearance", view: AppearancePane },
  { id: "accessibility", label: "Accessibility", view: AccessibilityPane },
  { id: "account", label: "Account", view: AccountPane },
] as const

// The shared collapsible sidebar beside one pane, so the toolbar keeps its sidebar toggle.
// Phones show the list at /settings and one full-height pane per section URL, with Back to the list.
export function SettingsView({ pane }: { pane: string }) {
  const current = sections.find((item) => item.id === pane)
  const identity = useQuery(identityQuery)
  const isPhone = useIsPhone()
  const list = (
    <SourceList
      aria-label="Settings sections"
      className="h-full pt-(--s3) max-[760px]:w-full max-[760px]:overflow-y-auto"
    >
      {sections.map((item) => (
        <SourceRow
          key={item.id}
          render={<Link to="/settings/$pane" params={{ pane: item.id }} />}
          selected={item.id === pane}
        >
          {item.label}
        </SourceRow>
      ))}
    </SourceList>
  )
  const body = (
    <div className={cn("flex h-full min-h-0 min-w-0 flex-1 flex-col gap-(--s6) overflow-y-auto p-(--s6) max-[760px]:scroll-pb-[calc(var(--tab-h)+var(--safe-b))]", current && "wide" in current ? "max-w-270" : "max-w-160")}>
      {current ? (
        <Suspense fallback={<RowSkeleton lines={2} />}><current.view /></Suspense>
      ) : (
        <Empty title="Settings section not found">Choose a section from the list.</Empty>
      )}
    </div>
  )
  return (
    <>
      <ToolbarTitle title={isPhone && current ? current.label : "Settings"} subtitle={identity.data?.node.name} />
      {isPhone ? (
        <div className="flex h-full min-h-0 min-w-0 flex-col">{pane ? body : list}</div>
      ) : (
        <ListDetail list={list}>{body}</ListDetail>
      )}
    </>
  )
}
