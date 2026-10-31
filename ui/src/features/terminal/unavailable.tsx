import { Banner } from "@/components/kit/feedback"
import { StatusSlot } from "@/features/status/status-bar"

export type Unavailable = { message: string; status: string }

// Port of app.js:1193-1202. The copy is the legacy copy; sessions here never open a socket.
export function unavailableTerminal(session: { harness: string; state: { interactive: boolean } } | undefined, stale: boolean): Unavailable | undefined {
  const agy = session?.harness === "agy"
  const nativeStdio = session?.state.interactive === false
  if (!agy && !nativeStdio && !stale) return undefined
  const message = agy
    ? "Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked."
    : nativeStdio
      ? "Native stdio · browser input unavailable"
      : "Offline · showing last-known session metadata"
  const status = stale ? "Offline · last-known session" : nativeStdio ? "Native stdio · input unavailable" : "Desktop session · local interaction"
  return { message, status }
}

export function UnavailableTerminal({ unavailable }: { unavailable: Unavailable }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <StatusSlot>{unavailable.status}</StatusSlot>
      <Banner>{unavailable.message}</Banner>
    </div>
  )
}
