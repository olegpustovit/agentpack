import { Link } from "@tanstack/react-router"
import { Empty } from "@/components/kit/feedback"
import { SourceList, SourceRow } from "@/components/kit/list"
import { AccountPane } from "./account-pane"
import { AppearancePane, GeneralPane, SharingPane } from "./panes"
import { ReplicationPane } from "./replication-pane"

const sections = [
  { id: "general", label: "General", view: GeneralPane },
  { id: "sharing", label: "Sharing", view: SharingPane },
  { id: "replication", label: "Replication", view: ReplicationPane },
  { id: "appearance", label: "Appearance", view: AppearancePane },
  { id: "account", label: "Account", view: AccountPane },
] as const

// A 180 px source list beside one pane. On phones the list stacks above the pane.
export function SettingsView({ pane }: { pane: string }) {
  const current = sections.find((item) => item.id === pane)
  return (
    <div className="flex h-full min-h-0 min-w-0 max-[760px]:flex-col">
      <SourceList
        aria-label="Settings sections"
        className="w-45 shrink-0 border-r-[0.5px] border-(--sep) bg-(--chrome) p-(--s2) max-[760px]:w-full max-[760px]:border-r-0 max-[760px]:border-b-[0.5px]"
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
      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-(--s6)">
        {current ? (
          <current.view />
        ) : (
          <Empty title="Settings section not found">Choose a section from the list.</Empty>
        )}
      </div>
    </div>
  )
}
