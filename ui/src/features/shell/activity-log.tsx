import type { RefObject } from "react"
import { CircleAlert, CircleCheck, X } from "lucide-react"
import { Popover, PopoverContent } from "@/components/ui/popover"
import { Button } from "@/components/ui/button"
import { clearActivity, dismissActivity, type Activity } from "@/lib/hud"

function ago(at: number, now: number) {
  const minutes = Math.round((now - at) / 60_000)
  return minutes < 1 ? "Just now" : minutes < 60 ? `${minutes} min ago` : new Date(at).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
}

// Loaded on first open so Popover stays out of the shell's initial bundle.
export default function ActivityLog({
  anchor,
  open,
  onOpenChange,
  log,
}: {
  anchor: RefObject<HTMLButtonElement | null>
  open: boolean
  onOpenChange: (open: boolean) => void
  log: Activity[]
}) {
  const now = Date.now()
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverContent anchor={anchor} align="end" className="w-90 gap-0 rounded-(--r-pop) bg-(--raised) p-(--s2) shadow-(--e-pop) ring-0">
        <div className="flex items-center px-(--s3) pt-(--s2) pb-(--s3)">
          <p className="flex-1 text-(length:--fs-sm) font-semibold text-(--fg2)">Activity</p>
          <Button size="sm" variant="ghost" onClick={clearActivity}>Clear</Button>
        </div>
        <ul className="flex max-h-80 flex-col overflow-y-auto">
          {log.map((item) => (
            <li key={item.id} className="flex min-w-0 items-start gap-(--s3) rounded-(--r-row) px-(--s3) py-(--s2) hover:bg-(--hover)">
              <span className="pt-0.5">
                {item.tone === "failed" ? <CircleAlert aria-hidden className="size-4 text-(--danger)" /> : <CircleCheck aria-hidden className="size-4 text-(--ok)" />}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block text-(length:--fs) leading-snug">{item.message}</span>
                <span className="block text-(length:--fs-xs) text-(--fg3)">{ago(item.at, now)}</span>
              </span>
              {item.tone === "failed" && item.retry && <Button size="sm" onClick={() => { dismissActivity(item.id); item.retry?.() }}>Retry</Button>}
              {item.tone === "failed" && (
                <Button size="icon" variant="ghost" aria-label={`Dismiss ${item.message}`} onClick={() => dismissActivity(item.id)}><X aria-hidden /></Button>
              )}
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
