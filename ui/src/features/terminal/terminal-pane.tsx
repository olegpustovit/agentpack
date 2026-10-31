import "@xterm/xterm/css/xterm.css"
import { useEffect, useRef, useState, useSyncExternalStore } from "react"
import { Minus, Plus } from "lucide-react"
import { RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { ContextMenuSeparator } from "@/components/ui/context-menu"
import { StatusSlot } from "@/features/status/status-bar"
import { harnessName } from "@/lib/format"
import type { SessionInfo } from "@/lib/api-types"
import { zoomPercent } from "./fit"
import { terminalPool } from "./pool"
import { TerminalSession } from "./terminal-session"

// xterm is imported only here: the session route loads this module with React.lazy.
export default function TerminalPane({
  machine,
  sessionId,
  info,
  listed,
}: {
  machine: string
  sessionId: string
  info?: SessionInfo
  // False only once the sessions list has loaded without this session.
  listed: boolean
}) {
  const [terminal] = useState(() => terminalPool.get(machine, sessionId, () => new TerminalSession(machine, sessionId)))
  const stageRef = useRef<HTMLDivElement>(null)
  const snapshot = useSyncExternalStore(terminal.subscribe, terminal.getSnapshot)

  useEffect(() => {
    const stage = stageRef.current
    if (!stage) return
    terminal.attach(stage)
    // Unmount only detaches: the pool keeps the session, and its socket, for a quick return.
    return () => terminal.detach()
  }, [terminal])

  useEffect(() => {
    terminal.setListed(listed)
  }, [terminal, listed])

  // Shown only while a session is attached and its harness is known from the list.
  const parts = info
    ? [harnessName(info.harness), info.mode || "default", `${snapshot.grid.cols} × ${snapshot.grid.rows}`, snapshot.sharedInput && "Shared input", snapshot.mouseOwned && "Mouse: app · ⇧ drag to select"]
    : []

  return (
    <div className="relative flex h-full min-h-0 flex-col bg-(--terminal-bg)">
      {info && <StatusSlot>{parts.filter(Boolean).join(" · ")}</StatusSlot>}
      <RowContextMenu>
        <RowContextMenuTrigger className="relative min-h-0 flex-1">
          <div ref={stageRef} aria-busy={snapshot.busy} className="absolute inset-0 overflow-auto transition-opacity aria-busy:opacity-60" />
        </RowContextMenuTrigger>
        <RowContextMenuContent>
          <RowContextMenuItem onClick={() => terminal.copy()}>Copy</RowContextMenuItem>
          <RowContextMenuItem onClick={() => terminal.paste()}>Paste</RowContextMenuItem>
          <ContextMenuSeparator />
          <RowContextMenuItem onClick={() => terminal.selectAll()}>Select All</RowContextMenuItem>
          <RowContextMenuItem onClick={() => terminal.clearSelection()}>Clear Selection</RowContextMenuItem>
        </RowContextMenuContent>
      </RowContextMenu>
      {snapshot.banner && (
        <p role="status" className="absolute top-(--s2) left-1/2 -translate-x-1/2 rounded-(--r-pop) bg-(--chrome) px-(--s3) py-(--s1) text-(--fs-sm) text-(--fg)">
          {snapshot.banner}
        </p>
      )}
      <div className="absolute right-(--s2) bottom-(--s2) flex items-center gap-(--s1) rounded-(--r-pop) bg-(--chrome) p-(--s1) text-(--fs-sm)">
        <Button variant="ghost" size="sm" aria-pressed={snapshot.fit} onClick={() => terminal.fitToStage()}>
          Fit
        </Button>
        <Button variant="ghost" size="sm" onClick={() => terminal.actualSize()}>
          1:1
        </Button>
        <Button variant="ghost" size="icon" aria-label="Smaller" onClick={() => terminal.zoom(-1)}>
          <Minus />
        </Button>
        <span className="w-[4ch] text-center tabular-nums">{zoomPercent(snapshot.fontSize)}%</span>
        <Button variant="ghost" size="icon" aria-label="Larger" onClick={() => terminal.zoom(1)}>
          <Plus />
        </Button>
      </div>
    </div>
  )
}
