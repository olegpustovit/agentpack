import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ToolbarSlot } from "@/features/shell/toolbar-slot"
import type { SessionView } from "./rules"
import { VIEW_SWITCH_RESUME_TOOLTIP, sessionViewFromTransport, viewSwitchNeedsResume } from "./view-switch"
import type { SessionInfo } from "@/lib/api-types"

export function SessionViewSwitch({
  info,
  onSwitch,
  pending,
}: {
  info?: SessionInfo
  onSwitch: (view: SessionView) => void
  pending?: boolean
}) {
  if (!info) return null
  const current = sessionViewFromTransport(info.transport)
  const blocked = viewSwitchNeedsResume(info)

  function choose(value: unknown[]) {
    const next = value[0]
    if (next !== "chat" && next !== "terminal") return
    if (next === current || pending) return
    onSwitch(next)
  }

  const control = (
    <SegmentedControl value={[current]} onValueChange={choose} aria-label="Session view" className="w-44">
      <SegmentedControlItem value="chat" className="flex-1" disabled={blocked && current !== "chat"}>
        Chat
      </SegmentedControlItem>
      <SegmentedControlItem value="terminal" className="flex-1" disabled={blocked && current !== "terminal"}>
        Terminal
      </SegmentedControlItem>
    </SegmentedControl>
  )

  return (
    <ToolbarSlot>
      <span className="max-[760px]:hidden">
        {blocked ? (
          <Tooltip>
            <TooltipTrigger render={<span className="inline-flex cursor-default" />}>{control}</TooltipTrigger>
            <TooltipContent>{VIEW_SWITCH_RESUME_TOOLTIP}</TooltipContent>
          </Tooltip>
        ) : (
          control
        )}
      </span>
    </ToolbarSlot>
  )
}
