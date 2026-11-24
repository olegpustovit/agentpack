import { useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { StatusDot } from "@/components/kit/status-dot"
import { Button, buttonVariants } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useEventsConnected, usePausedEvents } from "@/features/events/use-events"
import { nodesQuery, topologyQuery, useMachines } from "@/features/machines/queries"
import { ApiError } from "@/lib/api"
import { plural, relativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

export function ConnectionItem() {
  const live = useEventsConnected()
  const paused = usePausedEvents()
  const nodes = useQuery(nodesQuery(live))
  const machines = useMachines()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  // Last contact comes from topology, fetched only while the popover is open.
  const topology = useQuery({ ...topologyQuery(live), enabled: open })

  // A network failure (not an HTTP status) means the gateway itself did not answer.
  const unreachable = nodes.error !== null && !(nodes.error instanceof ApiError)
  const offline = machines.filter((machine) => !machine.online).length
  // Healthy is the normal state and takes no space.
  if (!unreachable && !paused && offline === 0) return null
  const offlineLabel = offline > 0 ? `${plural(offline, "machine")} offline` : undefined
  const label = unreachable ? "Gateway unreachable" : [paused && "Live updates paused", offlineLabel].filter(Boolean).join(" · ")
  const lastUpdate = paused?.lastReceivedAt
    ? new Date(paused.lastReceivedAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
    : undefined

  return (
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          render={<button type="button" title={label} className="activity-in inline-flex h-(--ctl-h) min-w-0 items-center gap-(--s3) rounded-full px-(--s3) text-(length:--fs-sm) text-(--fg2) hover:bg-(--hover)" />}
        >
          <span
            aria-hidden
            className={cn(
              "inline-block size-(--s3) shrink-0 rounded-full border",
              "border-dashed border-(--fg3)", // shown only when something is down
            )}
          />
          <span className="truncate">
            {label}
            {!unreachable && lastUpdate && <span className="max-[760px]:hidden">{` · last event ${lastUpdate}`}</span>}
          </span>
        </PopoverTrigger>
        <PopoverContent side="bottom" align="end" className="w-70 rounded-(--r-pop) bg-(--raised) p-(--s4) shadow-(--e-pop) ring-0">
          {paused && (
            <div className="flex flex-col items-start gap-(--s2)">
              <p className="text-(length:--fs-sm) text-(--fg2)">
                {lastUpdate ? `Last live update ${lastUpdate}. Reconnecting automatically.` : "Waiting for a live connection. Reconnecting automatically."}
              </p>
              <Button variant="ghost" size="sm" onClick={() => { paused.reconnect(); setOpen(false) }}>Reconnect now</Button>
            </div>
          )}
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
  )
}
