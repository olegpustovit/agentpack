import { createFileRoute } from "@tanstack/react-router"
import { looseSearch } from "@/lib/search"

const known = looseSearch({ range: "text", metric: "text", project: "text", machine: "text", agent: "text" })

// range is day, week or month; back counts periods before the current one; metric picks the chart's measure;
// project, machine and agent filter the rows. /usage/limits reads its own machine.
// back stays a number, so the URL reads ?back=1 rather than a quoted string.
export const Route = createFileRoute("/_app/usage")({
  validateSearch: (input: Record<string, unknown>) => {
    const { back, ...rest }: { back?: unknown } & ReturnType<typeof known> = known(input)
    const periods = Number(back)
    return Number.isInteger(periods) && periods > 0 ? { ...rest, back: periods } : (rest as typeof rest & { back?: number })
  },
})
