import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react"
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query"
import { LOCAL } from "@/lib/api"
import { configQuery } from "@/lib/config"
import type { ConversationPage } from "@/features/conversation/queries"

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
  agent_snapshots: [["agent-snapshot"]],
  omp_settings: [["omp-settings"]],
  // Web Push state feeds no query: this device reads its subscription from the browser.
  push_keys: [],
  push_subscriptions: [],
}
const ALL_STATE_FAMILIES = [["identity"], ["topology"], ["m", LOCAL, "sessions"], ["m", LOCAL, "limits"], ["m", LOCAL, "usage"], ["nodes"], ["diagnostics"], ["replication"], ["mcp-credentials"], ["omp-settings"], ["agent-snapshot"]]

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

type ConversationSubscription = {
  machine: string
  sessionId: string
  onEntry: (data: string) => void
  onTruncated: () => void
  onConnection: (connected: boolean) => void
  onFallback: () => void
}

const ConversationEventsContext = createContext<((subscription: ConversationSubscription) => () => void) | undefined>(undefined)

export function useConversationEvents() {
  return useContext(ConversationEventsContext)
}

// Mounted once in _app. A chat subscription changes the channels on this same browser stream.
export function EventsProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const multiplexed = useQuery(configQuery).data?.conversation_events === true
  const [connected, setConnected] = useState(false)
  const [conversation, setConversation] = useState<ConversationSubscription>()
  const catchUp = useRef(false)
  const subscribe = useCallback((subscription: ConversationSubscription) => {
    setConversation(subscription)
    return () => setConversation((current) => current === subscription ? undefined : current)
  }, [])
  useEffect(() => {
    let source: EventSource | null = null
    let retry: ReturnType<typeof setTimeout> | undefined
    const chat = multiplexed ? conversation : undefined
    const connection = (open: boolean) => {
      setConnected(open)
      chat?.onConnection(open)
    }
    const close = () => {
      clearTimeout(retry)
      source?.close()
      source = null
      connection(false)
    }
    const open = () => {
      if (source) return
      let path = "/api/events"
      if (chat) {
        const key = ["conversation", chat.machine, chat.sessionId]
        const since = queryClient.getQueryData<ConversationPage>(key)?.entries.at(-1)?.seq ?? 0
        const query = new URLSearchParams({ session: chat.sessionId, owner: chat.machine, since: String(since) })
        path += `?${query}`
      }
      source = new EventSource(path)
      source.onopen = () => {
        connection(true)
        if (catchUp.current) {
          catchUp.current = false
          for (const key of stateFamilies(undefined)) invalidate(queryClient, key)
        }
      }
      source.onerror = () => {
        connection(false)
        catchUp.current = true
        if (chat) {
          close()
          if (!document.hidden) retry = setTimeout(open, 1000)
        }
      }
      source.onmessage = (event: MessageEvent<string>) =>
        dispatch(queryClient, JSON.parse(event.data) as EventMessage)
      if (chat) {
        source.addEventListener("conversation", (event: MessageEvent<string>) => chat.onEntry(event.data))
        source.addEventListener("conversation_truncated", chat.onTruncated)
        source.addEventListener("conversation_fallback", chat.onFallback)
      }
    }
    const onVisibility = () => {
      if (document.hidden) {
        catchUp.current = true
        close()
      } else {
        open()
      }
    }
    onVisibility()
    document.addEventListener("visibilitychange", onVisibility)
    return () => {
      document.removeEventListener("visibilitychange", onVisibility)
      close()
    }
  }, [conversation, multiplexed, queryClient])
  return (
    <EventsContext value={connected}>
      <ConversationEventsContext value={multiplexed ? subscribe : undefined}>{children}</ConversationEventsContext>
    </EventsContext>
  )
}
