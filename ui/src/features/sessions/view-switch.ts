import type { SessionInfo } from "@/lib/api-types"

// /terminal reopens a chat as a terminal only when nothing is in flight, including a chat still starting.
export function viewSwitchNeedsResume(info: SessionInfo): boolean {
  return info.transport === "acp"
    ? info.conversation_state === "working" || info.conversation_state === "needs_you" || (info.conversation_state === "starting" && !info.ended_at)
    : info.state.running
}
