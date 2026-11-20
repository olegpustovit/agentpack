import { createContext, useContext, useEffect, useState, type ReactNode } from "react"
import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { LOCAL } from "@/lib/api"

type EventMessage = { topic: string; node?: string; tables?: string[] }

// What each replicated table feeds. A state change refetches only these; chat tables feed the chat stream alone.
const STATE_FAMILIES: Record<string, string[][]> = {
  // Usage counters arrive through usage_hourly, written in the same transaction as each turn.
  sessions: [["m", LOCAL, "sessions"]],
  limits: [["m", LOCAL, "limits"]],
  usage_hourly: [["m", LOCAL, "usage"]],
  limit_samples: [["m", LOCAL, "usage"]],
  users: [["identity"]],
  node_settings: [["identity"], ["nodes"], ["topology"]],
  mesh_nodes: [["nodes"], ["topology"], ["replication"]],
  mesh_settings: [["nodes"], ["topology"], ["replication"]],
  issues: [["diagnostics"]],
  issue_counts: [["diagnostics"]],
  conversation_events: [],
  archive_blobs: [],
  archive_edits: [],
  archive_event_clocks: [],
  blob_locations: [],
  mcp_credentials: [["mcp-credentials"]],
  mcp_credential_approvals: [["mcp-credentials"]],
  omp_settings: [["omp-settings"]],
  // Web Push state feeds no query: this device reads its subscription from the browser.
  push_keys: [],
  push_subscriptions: [],
}
const ALL_STATE_FAMILIES = [["identity"], ["topology"], ["m", LOCAL, "sessions"], ["m", LOCAL, "limits"], ["m", LOCAL, "usage"], ["nodes"], ["diagnostics"], ["replication"], ["mcp-credentials"], ["omp-settings"]]

// Unknown tables, or an older gateway that sends none, refetch every family.
export function stateFamilies(tables: string[] | undefined): string[][] {
  if (!tables || tables.some((table) => !(table in STATE_FAMILIES))) return ALL_STATE_FAMILIES
  const keys = new Map<string, string[]>()
  for (const table of tables) for (const key of STATE_FAMILIES[table]!) keys.set(key.join("\0"), key)
  return [...keys.values()]
}

const EventsContext = createContext(false)

// True while the /api/events stream is open. Polling queries read it to stop their timers.
export function useEventsConnected(): boolean {
  return useContext(EventsContext)
}

function invalidate(queryClient: QueryClient, queryKey: string[]) {
  void queryClient.invalidateQueries({ queryKey, refetchType: "active" })
}

function dispatch(queryClient: QueryClient, msg: EventMessage) {
  if (msg.topic === "state") {
    for (const key of stateFamilies(msg.tables)) invalidate(queryClient, key)
    return
  }
  if (msg.node && msg.node !== LOCAL && (msg.topic === "sessions" || msg.topic === "limits")) {
    invalidate(queryClient, ["m", LOCAL, msg.topic])
  }
  if (msg.topic === "replication") invalidate(queryClient, ["replication"])
  if (msg.topic === "nodes") {
    invalidate(queryClient, ["nodes"])
    invalidate(queryClient, ["topology"])
    invalidate(queryClient, ["replication"])
    return
  }
  // An open chat follows its own stream, so a sessions change never refetches conversations.
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
