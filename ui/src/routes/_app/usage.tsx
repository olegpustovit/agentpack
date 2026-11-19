import { createFileRoute, Outlet } from "@tanstack/react-router"
import { looseSearch } from "@/lib/search"
import { ListDetail } from "@/features/shell/list-detail"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { UsageSidebar } from "@/features/usage/usage-sidebar"

const known = looseSearch({ range: "text", metric: "text" })

// range is day, week or month; back counts periods before the current one; metric picks the chart's measure.
// back stays a number, so the URL reads ?back=1 rather than a quoted string.
export const Route = createFileRoute("/_app/usage")({
  validateSearch: (input: Record<string, unknown>) => {
    const { back, ...rest }: { back?: unknown } & ReturnType<typeof known> = known(input)
    const periods = Number(back)
    return Number.isInteger(periods) && periods > 0 ? { ...rest, back: periods } : (rest as typeof rest & { back?: number })
  },
  component: UsageLayout,
})

// A phone has no room for the source list: the overview drills in through its legend and lists.
function UsageLayout() {
  return useIsPhone() ? (
    <Outlet />
  ) : (
    <ListDetail list={<UsageSidebar />}>
      <Outlet />
    </ListDetail>
  )
}
