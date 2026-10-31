import { Banner } from "@/components/kit/feedback"

// Sessions with this message have no terminal to attach to, so they never open a socket.
export function unavailableMessage(session: { state: { interactive: boolean } } | undefined, stale: boolean): string | undefined {
  if (session?.state.interactive === false) return "Native stdio · browser input unavailable"
  return stale ? "Offline · showing last-known session metadata" : undefined
}

export function UnavailableTerminal({ message }: { message: string }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <Banner>{message}</Banner>
    </div>
  )
}
