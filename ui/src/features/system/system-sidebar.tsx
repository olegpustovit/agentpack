import { useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useMatchRoute } from "@tanstack/react-router"
import { ChartColumn, Gauge, Network, type LucideIcon } from "lucide-react"
import { GroupHeader, SearchField, SourceList, SourceRow } from "@/components/kit/list"
import { tightestWindow, useLimitGroups } from "@/features/limits/model"
import { MachineRow } from "@/features/machines/machine-list"
import { identityQuery, useAgentpackVersions, useDiscovering, useMachineQueries, useMachines, useReplicatedQueries } from "@/features/machines/queries"
import { projectsQuery, sessionsQuery } from "@/features/sessions/queries"
import { groups, matches, sections, useBadges, type SettingsSection } from "@/features/settings/sections"
import { statusQuery } from "@/features/status/queries"
import { cn } from "@/lib/utils"

const icon = "size-(--fs) shrink-0 opacity-70"
const trailing = "ml-auto flex shrink-0 items-center gap-(--s2) text-(length:--fs-sm)"
// A flex column shrinks its rows once they overflow; these keep their height and the list scrolls.
const row = "shrink-0"

function Label({ icon: Icon, label }: { icon: LucideIcon; label: string }) {
  return (
    <>
      <Icon aria-hidden className={icon} />
      <span className="min-w-0 truncate">{label}</span>
    </>
  )
}

// The one System list: usage, settings by scope, then machines, last because only they are unbounded.
export function SystemSidebar() {
  const matchRoute = useMatchRoute()
  const underUsage = Boolean(matchRoute({ to: "/usage" }))
  const underMachines = Boolean(matchRoute({ to: "/machines", fuzzy: true }))
  const [query, setQuery] = useState("")
  const list = useRef<HTMLDivElement>(null)

  const badges = useBadges()
  const identity = useQuery(identityQuery).data
  const status = useQuery(statusQuery).data
  const email = status?.email || identity?.auth || ""
  const tightest = tightestWindow(useLimitGroups().groups)
  const machines = useMachines()
  const discovering = useDiscovering()
  const sessions = useReplicatedQueries(sessionsQuery)
  // Asking every peer for its projects is worth it only while looking at machines.
  const projects = useMachineQueries(projectsQuery, underMachines)
  const versionOf = useAgentpackVersions()

  const matched = (label: string, keywords = "") => matches({ label, keywords }, query)
  const usage = matched("Usage", "tokens cost")
  const limits = matched("Limits", "quota remaining")
  const found = sections.filter((item) => matches(item, query))
  const account = found.find((item) => item.scope === "account")
  const topology = matched("Topology", "machines network mesh")
  const shownMachines = machines.filter((machine) => matched(machine.name))
  const none = !usage && !limits && !topology && found.length === 0 && shownMachines.length === 0

  const settingsRow = (item: SettingsSection) => {
    const badge = badges[item.id]
    return (
      <SourceRow key={item.id} className={row} render={<Link to="/settings/$pane" params={{ pane: item.id }} />} selected={Boolean(matchRoute({ to: "/settings/$pane", params: { pane: item.id } }))}>
        <Label icon={item.icon} label={item.label} />
        {badge && (
          <span className={cn(trailing, "opacity-70")}>
            {badge.attention && <span aria-hidden className="size-1.5 rounded-full bg-(--warn)" />}
            {badge.text}
          </span>
        )}
      </SourceRow>
    )
  }

  return (
    <div ref={list} className="flex h-full min-h-0 flex-col max-[760px]:w-full">
      <div className="flex shrink-0 px-(--s3) pt-(--s3) pb-(--s2)">
        <SearchField
          aria-label="Search system"
          placeholder="Search"
          value={query}
          onChange={(event) => setQuery(event.currentTarget.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && query) list.current?.querySelector<HTMLElement>("[data-source-row]")?.click()
            if (event.key === "Escape") setQuery("")
          }}
        />
      </div>
      <SourceList aria-label="System" aria-busy={discovering} className="flex-1">
        {/* Only the Usage page's own search carries over; Limits has a machine param of its own. */}
        {usage && (
          <SourceRow className={row} render={<Link to="/usage" search={underUsage ? true : {}} />} selected={underUsage}>
            <Label icon={ChartColumn} label="Usage" />
          </SourceRow>
        )}
        {limits && (
          <SourceRow className={row} render={<Link to="/usage/limits" search={{}} />} selected={Boolean(matchRoute({ to: "/usage/limits" }))}>
            <Label icon={Gauge} label="Limits" />
            {tightest && <span className={cn(trailing, "text-(--fg3) tabular-nums sel:text-(--accent-fg)", tightest.left <= 20 && "text-(--warn-word)")} title={`${Math.round(tightest.left)}% left`}>{`${Math.round(tightest.left)}%`}</span>}
          </SourceRow>
        )}
        {account && (
          <div role="group" aria-label="Settings" className="mt-(--s3) flex shrink-0 flex-col">
            <GroupHeader>Settings</GroupHeader>
            <SourceRow className="h-(--row-h-2) shrink-0" render={<Link to="/settings/$pane" params={{ pane: account.id }} />} selected={Boolean(matchRoute({ to: "/settings/$pane", params: { pane: account.id } }))}>
              <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-full bg-(--accent-fill) text-(length:--fs-sm) font-semibold text-(--on-accent) uppercase">{email.slice(0, 1) || "?"}</span>
              <span className="flex min-w-0 flex-col leading-tight">
                <span className="truncate">{email || "Account"}</span>
                <span className="truncate text-(length:--fs-xs) opacity-70">Account</span>
              </span>
            </SourceRow>
          </div>
        )}
        {groups.map((group) => {
          const items = found.filter((item) => item.scope === group.scope)
          if (!items.length) return null
          const name = identity?.node.name
          const title = `Settings · ${group.title}${group.scope === "machine" && name ? ` · ${name}` : ""}`
          return (
            <div key={group.scope} role="group" aria-label={title} className="mt-(--s3) flex shrink-0 flex-col">
              <GroupHeader><span className="truncate">{title}</span></GroupHeader>
              {items.map(settingsRow)}
            </div>
          )
        })}
        {(topology || shownMachines.length > 0 || discovering) && (
          <div role="group" aria-label="Machines" className="mt-(--s3) flex shrink-0 flex-col">
            <GroupHeader>Machines</GroupHeader>
            {topology && (
              <SourceRow className={row} render={<Link to="/machines/topology" />} selected={Boolean(matchRoute({ to: "/machines/topology" }))}>
                <Label icon={Network} label="Topology" />
              </SourceRow>
            )}
            {discovering && <p className="px-(--s3) py-(--s2) text-(length:--fs-sm) text-(--fg2)">Discovering connected machines</p>}
            {shownMachines.map((machine) => (
              <MachineRow key={machine.id} machine={machine} sessions={sessions.byMachine[machine.id]?.data?.length} needsUpdate={projects.byMachine[machine.id]?.needsUpdate || versionOf(machine.id).outdated} />
            ))}
          </div>
        )}
        {none && !discovering && <p className="px-(--s3) py-(--s4) text-(length:--fs-sm) text-(--fg2)">{`Nothing matches “${query}”.`}</p>}
      </SourceList>
    </div>
  )
}
