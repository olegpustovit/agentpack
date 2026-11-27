import { lazy, Suspense, useEffect, useRef, useState } from "react"
import { CircleAlert, CircleCheck, History, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { dismissActivity, useActivity, type Activity } from "@/lib/hud"
import { cn } from "@/lib/utils"

const FRESH_MS = 4000
const ActivityLog = lazy(() => import("./activity-log"))

function Glyph({ item, idle }: { item: Activity; idle: boolean }) {
  if (idle) return <History aria-hidden className="size-4 text-(--fg2)" />
  return item.tone === "failed" ? <CircleAlert aria-hidden className="size-4 text-(--danger)" /> : <CircleCheck aria-hidden className="size-4 text-(--ok)" />
}

// The toolbar's activity viewer: the latest outcome appears in place, then settles into a history
// button. A failure stays expanded with Retry until it is handled. Nothing floats over content.
export function ActivityViewer() {
  const log = useActivity()
  const [now, setNow] = useState(() => Date.now())
  const [open, setOpen] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const latest = log[0]
  const fresh = latest !== undefined && (latest.tone === "failed" || now - latest.at < FRESH_MS)
  useEffect(() => {
    if (!latest || latest.tone === "failed") return
    setNow(Date.now())
    const timer = setTimeout(() => setNow(Date.now()), FRESH_MS + 50)
    return () => clearTimeout(timer)
  }, [latest])
  if (!latest) return null
  const failed = fresh && latest.tone === "failed"
  const undoable = fresh && !failed && latest.undo

  return (
    <>
      <div
        data-slot="activity"
        data-fresh={fresh || undefined}
        className={cn(
          "flex h-(--ctl-h) min-w-0 shrink items-center rounded-full transition-[background-color,box-shadow] duration-(--dur-2) ease-(--ease)",
          fresh && "bg-(--control) shadow-(--shadow-ctl)",
          failed && "max-[760px]:order-last max-[760px]:mb-(--s3) max-[760px]:basis-full",
        )}
      >
        <button
          ref={triggerRef}
          type="button"
          aria-expanded={open}
          aria-haspopup="dialog"
          aria-label={fresh ? `${latest.message}. Show activity` : "Activity"}
          onClick={() => {
            setLoaded(true)
            setOpen((current) => !current)
          }}
          className="flex h-full min-w-0 items-center gap-(--s3) rounded-full px-(--s3) outline-none hover:bg-(--hover) focus-visible:ring-2 focus-visible:ring-(--accent)"
        >
          {/* Keyed by the event, so each new outcome eases in once; the text then collapses into the icon. */}
          <span key={latest.id} className="activity-in flex min-w-0 items-center gap-(--s3)">
            <Glyph item={latest} idle={!fresh} />
            <span
              aria-live="polite"
              title={latest.message}
              className={cn(
                "min-w-0 truncate text-(length:--fs-sm) transition-[max-width,opacity] duration-(--dur-3) ease-(--ease)",
                fresh ? cn("max-w-80 opacity-100", !failed && "max-[760px]:max-w-0 max-[760px]:opacity-0", failed && "max-[760px]:max-w-none") : "max-w-0 opacity-0",
                "text-(--fg)",
              )}
            >
              {latest.message}
            </span>
          </span>
        </button>
        {undoable && (
          <span className="activity-in ms-auto flex shrink-0 items-center pe-(--s1)">
            <Button size="sm" className="rounded-full" onClick={() => { dismissActivity(latest.id); latest.undo?.() }}>Undo</Button>
          </span>
        )}
        {failed && (
          <span className="activity-in ms-auto flex shrink-0 items-center gap-(--s1) pe-(--s1)">
            {latest.retry && <Button size="sm" className="rounded-full" onClick={() => { dismissActivity(latest.id); latest.retry?.() }}>Retry</Button>}
            <Button size="icon" variant="ghost" className="size-(--ctl-h-sm) rounded-full" aria-label="Dismiss" onClick={() => dismissActivity(latest.id)}><X /></Button>
          </span>
        )}
      </div>
      {loaded && (
        <Suspense fallback={null}>
          <ActivityLog anchor={triggerRef} open={open} onOpenChange={setOpen} log={log} />
        </Suspense>
      )}
    </>
  )
}
