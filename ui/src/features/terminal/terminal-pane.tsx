import "@xterm/xterm/css/xterm.css"
import { useEffect, useRef, useState, useSyncExternalStore } from "react"
import { Minus, Plus } from "lucide-react"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { zoomPercent } from "./fit"
import { terminalPool } from "./pool"
import { TerminalSession } from "./terminal-session"
import { TerminalScreen } from "./terminal-screen"

const selectionModifier = typeof navigator !== "undefined" && /Mac/.test(navigator.platform) ? "⌥" : "⇧"
const zoomButton = "flex h-(--ctl-h-sm) min-w-(--ctl-h-sm) cursor-default items-center justify-center rounded-(--r-row) px-(--s3) text-(--fg2) aria-pressed:bg-(--control) aria-pressed:text-(--fg) aria-pressed:shadow-(--shadow-ctl)"

// xterm is imported only here: the session route loads this module with React.lazy.
export default function TerminalPane({
  machine,
  sessionId,
  title,
  subtitle,
  listed,
}: {
  machine: string
  sessionId: string
  title?: string
  subtitle?: string
  // False only once the sessions list has loaded without this session.
  listed: boolean
}) {
  const [terminal] = useState(() => terminalPool.get(machine, sessionId, () => new TerminalSession(machine, sessionId)))
  const isPhone = useIsPhone()
  const stageRef = useRef<HTMLDivElement>(null)
  const snapshot = useSyncExternalStore(terminal.subscribe, terminal.getSnapshot)

  useEffect(() => {
    const stage = stageRef.current
    if (!stage) return
    terminal.attach(stage)
    // Unmount only detaches: the pool keeps the session, and its socket, for a quick return.
    return () => terminal.detach(stage)
  }, [terminal])

  useEffect(() => {
    terminal.setListed(listed)
  }, [terminal, listed])

  return (
    <div className="relative flex h-full min-h-0 flex-col bg-(--terminal-bg)">
      {/* The one terminal fact worth a line: mouse capture changes how selection works. */}
      {title && <ToolbarTitle title={title} subtitle={snapshot.mouseOwned ? [subtitle, `Mouse: app · ${selectionModifier} drag to select`].filter(Boolean).join(" · ") : subtitle} />}
      {!isPhone && (
        <ToolbarSlot>
          <div role="group" aria-label="Terminal zoom" className="flex h-(--ctl-h) items-center rounded-(--r-ctl) bg-(--track) p-(--s1) text-(length:--fs-sm)">
            <button type="button" aria-pressed={snapshot.fit} onClick={() => terminal.fitToStage()} className={zoomButton}>
              Fit
            </button>
            <button type="button" aria-label="Smaller" onClick={() => terminal.zoom(-1)} className={zoomButton}>
              <Minus aria-hidden />
            </button>
            <span className="w-[5ch] text-center text-(--fg2) tabular-nums">{zoomPercent(snapshot.fontSize)}%</span>
            <button type="button" aria-label="Larger" onClick={() => terminal.zoom(1)} className={zoomButton}>
              <Plus aria-hidden />
            </button>
            <button type="button" onClick={() => terminal.actualSize()} className={zoomButton}>
              1:1
            </button>
          </div>
        </ToolbarSlot>
      )}
      <TerminalScreen stageRef={stageRef} busy={snapshot.busy} terminal={terminal} />
      {snapshot.banner && (
        <p role="status" className="absolute top-(--s2) left-1/2 -translate-x-1/2 rounded-(--r-pop) bg-(--chrome) px-(--s3) py-(--s1) text-(length:--fs-sm) text-(--fg)">
          {snapshot.banner}
        </p>
      )}
    </div>
  )
}
