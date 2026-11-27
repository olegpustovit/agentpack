import { Link, useMatchRoute } from "@tanstack/react-router"
import { ChartColumn, Gauge, Network, type LucideIcon } from "lucide-react"
import { GroupHeader, SourceList, SourceRow } from "@/components/kit/list"
import { sections, useBadges } from "@/features/settings/sections"

const icon = "size-(--fs) shrink-0 opacity-70"
// A flex column shrinks its rows once they overflow; these keep their height.
const row = "shrink-0"

function Label({ icon: Icon, label }: { icon: LucideIcon; label: string }) {
  return (
    <>
      <Icon aria-hidden className={icon} />
      <span className="min-w-0 truncate">{label}</span>
    </>
  )
}

// Six rows: what the mesh used, what is left, the machines, then the three settings people open.
export function SystemSidebar() {
  const matchRoute = useMatchRoute()
  const underUsage = Boolean(matchRoute({ to: "/usage" }))
  const badges = useBadges()
  const current = sections.find((item) => matchRoute({ to: "/settings/$pane", params: { pane: item.id } }))

  return (
    <SourceList aria-label="System" className="h-full pt-(--s3) max-[760px]:w-full">
      {/* Only the Usage page's own search carries over. */}
      <SourceRow className={row} render={<Link to="/usage" search={underUsage ? true : {}} />} selected={underUsage}>
        <Label icon={ChartColumn} label="Usage" />
      </SourceRow>
      <SourceRow className={row} render={<Link to="/usage/limits" />} selected={Boolean(matchRoute({ to: "/usage/limits" }))}>
        <Label icon={Gauge} label="Limits" />
      </SourceRow>
      <SourceRow className={row} render={<Link to="/machines/topology" />} selected={Boolean(matchRoute({ to: "/machines", fuzzy: true }))}>
        <Label icon={Network} label="Topology" />
      </SourceRow>
      <div role="group" aria-label="Settings" className="mt-(--s3) flex shrink-0 flex-col">
        <GroupHeader>Settings</GroupHeader>
        {sections.filter((item) => !item.parent).map((item) => (
          <SourceRow key={item.id} className={row} render={<Link to="/settings/$pane" params={{ pane: item.id }} />} selected={(current?.parent ?? current?.id) === item.id}>
            <Label icon={item.icon} label={item.label} />
            {badges[item.id] && (
              <span className="ml-auto flex shrink-0 items-center gap-(--s2) text-(length:--fs-sm) opacity-70">
                <span aria-hidden className="size-1.5 rounded-full bg-(--warn)" />
                {badges[item.id]}
              </span>
            )}
          </SourceRow>
        ))}
      </div>
    </SourceList>
  )
}
