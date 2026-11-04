import type { SessionInfo } from "@/lib/api-types"
import type { SessionView } from "./rules"

// Matches the macOS conversation prototype copy for a blocked terminal view switch.
export const VIEW_SWITCH_RESUME_TOOLTIP =
  "Stop this session or start a new one to open the other view. Conversation resume depends on the agent."

export function sessionViewFromTransport(transport?: string): SessionView {
  return transport === "acp" ? "chat" : "terminal"
}

export function viewSwitchNeedsResume(info: SessionInfo): boolean {
  return info.transport === "acp"
    ? info.conversation_state === "working" || info.conversation_state === "needs_you"
    : info.state.running
}
