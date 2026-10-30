import { createContext, useContext, useEffect, useState, type ReactNode } from "react"
import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { LOCAL } from "@/lib/api"

type EventMessage = { topic: string; node?: string }

const EventsContext = createContext(false)

// True while the /api/events stream is open. Polling queries read it to stop their timers.
export function useEventsConnected(): boolean {
  return useContext(EventsContext)
}

function invalidate(queryClient: QueryClient, queryKey: string[]) {
  void queryClient.invalidateQueries({ queryKey, refetchType: "active" })
}

function dispatch(queryClient: QueryClient, msg: EventMessage) {
  if (msg.topic === "nodes") {
    invalidate(queryClient, ["nodes"])
    invalidate(queryClient, ["topology"])
    return
  }
  invalidate(queryClient, ["m", msg.node || LOCAL, msg.topic])
}

// Mounted once in _app. The stream closes while the tab is hidden and reopens when it is visible.
export function EventsProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [connected, setConnected] = useState(false)
  useEffect(() => {
    let source: EventSource | null = null
    const open = () => {
      if (source) return
      source = new EventSource("/api/events")
      source.onopen = () => setConnected(true)
      source.onerror = () => setConnected(false)
      source.onmessage = (event: MessageEvent<string>) =>
        dispatch(queryClient, JSON.parse(event.data) as EventMessage)
    }
    const close = () => {
      source?.close()
      source = null
      setConnected(false)
    }
    const onVisibility = () => (document.hidden ? close() : open())
    onVisibility()
    document.addEventListener("visibilitychange", onVisibility)
    return () => {
      document.removeEventListener("visibilitychange", onVisibility)
      close()
    }
  }, [queryClient])
  return <EventsContext value={connected}>{children}</EventsContext>
}
