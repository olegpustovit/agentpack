import { Banner } from "@/components/kit/feedback"

// Port of app.js:1193-1202. The copy is the legacy copy; sessions here never open a socket.
export function unavailableMessage(session: { harness: string; state: { interactive: boolean } } | undefined, stale: boolean): string | undefined {
  const agy = session?.harness === "agy"
  const nativeStdio = session?.state.interactive === false
  if (!agy && !nativeStdio && !stale) return undefined
  return agy
    ? "Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked."
    : nativeStdio
      ? "Native stdio · browser input unavailable"
      : "Offline · showing last-known session metadata"
}

export function UnavailableTerminal({ message }: { message: string }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <Banner>{message}</Banner>
    </div>
  )
}
