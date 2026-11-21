import type { RefObject } from "react"
import { RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { ContextMenuSeparator } from "@/components/ui/context-menu"
import type { TerminalSession } from "./terminal-session"

export function TerminalScreen({ stageRef, busy, terminal }: { stageRef: RefObject<HTMLDivElement | null>; busy: boolean; terminal: TerminalSession }) {
  return (
    <RowContextMenu>
      <RowContextMenuTrigger className="relative min-h-0 flex-1">
        <div ref={stageRef} aria-busy={busy} className="absolute top-(--s2) right-0 bottom-0 left-(--s3) overflow-auto transition-opacity aria-busy:opacity-60" />
      </RowContextMenuTrigger>
      <RowContextMenuContent>
        <RowContextMenuItem onClick={() => terminal.copy()}>Copy</RowContextMenuItem>
        <RowContextMenuItem onClick={() => terminal.paste()}>Paste</RowContextMenuItem>
        <ContextMenuSeparator />
        <RowContextMenuItem onClick={() => terminal.selectAll()}>Select All</RowContextMenuItem>
        <RowContextMenuItem onClick={() => terminal.clearSelection()}>Clear Selection</RowContextMenuItem>
      </RowContextMenuContent>
    </RowContextMenu>
  )
}
