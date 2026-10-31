import { useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { StatusDot } from "@/components/kit/status-dot"
import { StatusItem } from "@/components/kit/window"
import { Button, buttonVariants } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useEventsConnected } from "@/features/events/use-events"
import { nodesQuery, topologyQuery, useMachines } from "@/features/machines/queries"
import { ApiError } from "@/lib/api"
import { plural, relativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

export function ConnectionItem() {
  const live = useEventsConnected()
  const nodes = useQuery(nodesQuery(live))
  const machines = useMachines()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  // Last contact comes from topology, fetched only while the popover is open.
  const topology = useQuery({ ...topologyQuery(live), enabled: open })

  // A network failure (not an HTTP status) means the gateway itself did not answer.
  const unreachable = nodes.error !== null && !(nodes.error instanceof ApiError)
  if (!unreachable && !nodes.data && !nodes.error) return null

  const offline = machines.filter((machine) => !machine.online).length
  const label = unreachable
    ? "Gateway unreachable"
    : [`Connected · ${plural(machines.length, "machine")}`, offline > 0 ? `${offline} offline` : null]
        .filter(Boolean)
        .join(" · ")

  return (
    <StatusItem>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          render={<button type="button" className="inline-flex min-w-0 items-center gap-(--s3) rounded-(--r-row) hover:bg-muted" />}
        >
          <span
            aria-hidden
            className={cn(
              "inline-block size-(--s3) shrink-0 rounded-full border",
              unreachable ? "border-dashed border-(--fg3)" : "border-(--ok) bg-(--ok)",
            )}
          />
          <span className="truncate">{label}</span>
        </PopoverTrigger>
        <PopoverContent side="top" align="start" className="w-70 p-(--s4)">
          <ul>
            {machines.map((machine) => {
              const peer = topology.data?.peers.find((item) => item.id === machine.id)
              return (
                <li key={machine.id} className="flex items-center justify-between gap-(--s4) py-(--s2)">
                  <span className="min-w-0 truncate">{machine.name}</span>
                  <StatusDot state={machine.online ? "running" : "offline"} />
                  <span className="text-(length:--fs-sm) text-(--fg2)">
                    {peer?.last_seen ? relativeTime(new Date(peer.last_seen)) : ""}
                  </span>
                </li>
              )
            })}
          </ul>
          <div className="flex items-center justify-between gap-(--s3) pt-(--s2)">
            <Button variant="ghost" size="sm" onClick={() => void queryClient.invalidateQueries()}>
              Refresh
            </Button>
            <Link to="/machines/topology" className={buttonVariants({ variant: "ghost", size: "sm" })}>
              View topology
            </Link>
          </div>
        </PopoverContent>
      </Popover>
    </StatusItem>
  )
}
