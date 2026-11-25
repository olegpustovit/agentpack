import { Suspense, useState, type ComponentType, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { Accessibility, Activity, Bell, Bot, Sparkles, Globe, KeyRound, Monitor, Palette, RefreshCw, Server, Users, type LucideIcon } from "lucide-react"
import { Empty, RowSkeleton } from "@/components/kit/feedback"
import { GroupHeader, SearchField, SourceList, SourceRow } from "@/components/kit/list"
import { useEventsConnected } from "@/features/events/use-events"
import { replicationQuery } from "@/features/library/queries"
import { identityQuery } from "@/features/machines/queries"
import { statusQuery } from "@/features/status/queries"
import { ListDetail } from "@/features/shell/list-detail"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { cn } from "@/lib/utils"
import { AccountPane } from "./account-pane"
import { connectionsQuery } from "./connections-model"
import { issuesQuery } from "./diagnostics-model"
import { NotificationsPane } from "./notifications-pane"
import { AccessibilityPane, AgentsPane, AppearancePane, ConnectionsPane, DiagnosticsPane, GeneralPane, OmpPane, SharingPane } from "./panes"
import { ReplicationPane } from "./replication-pane"

type Scope = "account" | "machine" | "mesh" | "browser"
type SettingsSection = { id: string; label: string; scope: Scope; icon: LucideIcon; view: ComponentType; keywords: string; about?: string; wide?: boolean }

// Grouped by where a setting lives, so every pane says who it affects (Apple HIG; VS Code machine scope).
const sections: SettingsSection[] = [
  { id: "account", label: "Account", scope: "account", icon: Users, view: AccountPane, keywords: "email sign out everywhere tailscale user login" },
  { id: "general", label: "General", scope: "machine", icon: Server, view: GeneralPane, keywords: "version platform hostname status stop gateway agents restart" },
  { id: "sharing", label: "Sharing", scope: "machine", icon: Globe, view: SharingPane, keywords: "public access https mcp address tailscale allow list funnel proxy" },
  { id: "diagnostics", label: "Diagnostics", scope: "machine", icon: Activity, view: DiagnosticsPane, keywords: "issues errors traces crash logs resolve" },
  { id: "replication", label: "Replication", scope: "mesh", icon: RefreshCw, view: ReplicationPane, keywords: "mesh peers machines enroll sync remove schema",
    about: "Enrolled machines share accounts, chat history, limits and Library data, and every member is fully trusted. Enroll on both machines to start syncing." },
  { id: "connections", label: "Connections", scope: "mesh", icon: KeyRound, view: ConnectionsPane, keywords: "mcp oauth login sign in credentials token header variable linear notion",
    about: "Sign in to a remote MCP server once and every machine can use it. With more than one login, projects use the default unless you pick another in New session." },
  { id: "agents", label: "Agents", scope: "mesh", icon: Sparkles, view: AgentsPane, keywords: "mcp agentpack tools subagents task helpers defaults new chats" },
  // Role tables need more than the form width.
  { id: "omp", label: "Oh My Pi", scope: "mesh", icon: Bot, view: OmpPane, keywords: "omp models roles thinking agent", wide: true,
    about: "One configuration for every Oh My Pi session. Changes apply to new chats. A setting you never change keeps each machine’s own value." },
  { id: "notifications", label: "Notifications", scope: "browser", icon: Bell, view: NotificationsPane, keywords: "push alerts badge needs you turn finishes limit offline" },
  { id: "appearance", label: "Appearance", scope: "browser", icon: Palette, view: AppearancePane, keywords: "theme dark light system" },
  { id: "accessibility", label: "Accessibility", scope: "browser", icon: Accessibility, view: AccessibilityPane, keywords: "screen reader terminal voiceover" },
]

const groups: { scope: Exclude<Scope, "account">; title: string }[] = [
  { scope: "machine", title: "This machine" },
  { scope: "mesh", title: "All machines" },
  { scope: "browser", title: "This browser" },
]

function scopeLine(scope: Scope, machine: string) {
  if (scope === "machine") return { icon: Server, text: `Applies to ${machine || "this machine"}` }
  if (scope === "mesh") return { icon: RefreshCw, text: "Shared with every enrolled machine" }
  if (scope === "browser") return { icon: Monitor, text: "Saved in this browser only" }
  return { icon: Users, text: "Your gateway account on every machine" }
}

function matches(item: SettingsSection, query: string) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  const text = `${item.label} ${item.keywords}`.toLowerCase()
  return words.every((word) => text.includes(word))
}

// Counts that need a look, shown on the row like System Settings' badges.
function useBadges(): Record<string, { text: string; attention: boolean } | undefined> {
  const live = useEventsConnected()
  const issues = useQuery({ ...issuesQuery("open"), refetchInterval: live ? false : 30_000 }).data
  const connections = useQuery(connectionsQuery(live)).data
  const replication = useQuery({ ...replicationQuery, refetchInterval: live ? false : 30_000 }).data
  const signIn = connections?.credentials.filter((item) => item.status === "needs_login").length ?? 0
  const stuck = (replication?.peers ?? []).filter((peer) => {
    const contact = replication?.contacts?.[peer.node]
    return contact?.error || contact?.needs_update || contact?.needs_trust || contact?.reachable === false
  }).length
  return {
    diagnostics: issues?.length ? { text: String(issues.length), attention: issues.some((issue) => issue.regressed) } : undefined,
    connections: signIn ? { text: `${signIn} to sign in`, attention: true } : undefined,
    replication: stuck ? { text: `${stuck} not syncing`, attention: true } : undefined,
  }
}

function Badge({ text, attention }: { text: string; attention: boolean }) {
  return (
    <span className="ml-auto flex shrink-0 items-center gap-(--s2) text-(length:--fs-sm) opacity-70">
      {attention && <span aria-hidden className="size-1.5 rounded-full bg-(--warn)" />}
      {text}
    </span>
  )
}

// The shared collapsible sidebar beside one pane, so the toolbar keeps its sidebar toggle.
// Phones show the list at /settings and one full-height pane per section URL, with Back to the list.
export function SettingsView({ pane }: { pane: string }) {
  const current = sections.find((item) => item.id === pane)
  const identity = useQuery(identityQuery)
  const status = useQuery(statusQuery)
  const isPhone = useIsPhone()
  const navigate = useNavigate()
  const badges = useBadges()
  const [query, setQuery] = useState("")
  const machine = identity.data?.node.name ?? ""
  const email = status.data?.email || identity.data?.auth || ""
  const found = sections.filter((item) => matches(item, query))

  const row = (item: SettingsSection, children?: ReactNode) => (
    <SourceRow
      key={item.id}
      render={<Link to="/settings/$pane" params={{ pane: item.id }} />}
      selected={item.id === pane}
      className={children ? "h-(--row-h-2)" : undefined}
    >
      {children ?? (
        <>
          <item.icon aria-hidden className="size-(--fs) shrink-0 opacity-70" />
          <span className="min-w-0 truncate">{item.label}</span>
          {badges[item.id] && <Badge {...badges[item.id]!} />}
        </>
      )}
    </SourceRow>
  )
  const account = sections[0]!
  const list = (
    <div className="flex h-full min-h-0 flex-col max-[760px]:w-full">
      <div className="px-(--s3) pt-(--s3) pb-(--s2)">
        <SearchField
          aria-label="Search settings"
          placeholder="Search"
          value={query}
          onChange={(event) => setQuery(event.currentTarget.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && found[0]) void navigate({ to: "/settings/$pane", params: { pane: found[0].id } })
            if (event.key === "Escape") setQuery("")
          }}
        />
      </div>
      <SourceList aria-label="Settings sections" className="flex-1 max-[760px]:overflow-y-auto">
        {!query && row(account, (
          <>
            <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-full bg-(--accent-fill) text-(length:--fs-sm) font-semibold text-(--on-accent) uppercase">{email.slice(0, 1) || "?"}</span>
            <span className="flex min-w-0 flex-col leading-tight">
              <span className="truncate">{email || "Account"}</span>
              <span className="truncate text-(length:--fs-xs) opacity-70">Account</span>
            </span>
          </>
        ))}
        {query && found.length === 0 && <p className="px-(--s3) py-(--s4) text-(length:--fs-sm) text-(--fg2)">{`No settings match “${query}”.`}</p>}
        {query && found.some((item) => item.scope === "account") && row(account)}
        {groups.map((group) => {
          const items = found.filter((item) => item.scope === group.scope)
          if (!items.length) return null
          return (
            <div key={group.scope} role="group" aria-label={group.title} className="mt-(--s3) flex flex-col">
              <GroupHeader>{group.scope === "machine" && machine ? `${group.title} · ${machine}` : group.title}</GroupHeader>
              {items.map((item) => row(item))}
            </div>
          )
        })}
      </SourceList>
    </div>
  )
  const scope = current && scopeLine(current.scope, machine)
  const body = (
    <div className="h-full min-h-0 min-w-0 flex-1 overflow-y-auto max-[760px]:scroll-pb-[calc(var(--tab-h)+var(--safe-b))]">
      <div className={cn("mx-auto flex w-full flex-col gap-(--s7) px-(--s6) pt-(--s7) pb-(--s9) max-[760px]:px-(--s5) max-[760px]:pt-(--s5)", current?.wide ? "max-w-270" : "max-w-170")}>
        {current && scope ? (
          <>
            <header className="flex flex-col gap-(--s2)">
              <h2 className="text-(length:--t-large) font-semibold max-[760px]:sr-only">{current.label}</h2>
              <p className="flex items-center gap-(--s2) text-(length:--fs-sm) text-(--fg2)">
                <scope.icon aria-hidden className="size-(--fs-sm)" />
                {scope.text}
              </p>
              {current.about && <p className="max-w-[72ch] text-(length:--fs-sm) text-(--fg2)">{current.about}</p>}
            </header>
            <Suspense fallback={<RowSkeleton lines={2} />}><current.view /></Suspense>
          </>
        ) : (
          <Empty title="Settings section not found">Choose a section from the list.</Empty>
        )}
      </div>
    </div>
  )
  return (
    <>
      <ToolbarTitle title={isPhone && current ? current.label : "Settings"} subtitle={machine} />
      {isPhone ? (
        <div className="flex h-full min-h-0 min-w-0 flex-col">{pane ? body : list}</div>
      ) : (
        <ListDetail list={list}>{body}</ListDetail>
      )}
    </>
  )
}
