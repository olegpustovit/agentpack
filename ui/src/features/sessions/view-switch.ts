import type { SessionInfo } from "@/lib/api-types"

// /terminal reopens a chat as a terminal only when nothing is in flight.
export function viewSwitchNeedsResume(info: SessionInfo): boolean {
  return info.transport === "acp"
    ? info.conversation_state === "working" || info.conversation_state === "needs_you"
    : info.state.running
}
