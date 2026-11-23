import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react"
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query"
import { LOCAL } from "@/lib/api"
import { configQuery } from "@/lib/config"
import type { ConversationPage } from "@/features/conversation/queries"

type EventMessage = { topic: string; node?: string; tables?: string[] }

// What each replicated table feeds. A state change refetches only these; chat tables feed the chat stream alone.
// Every replicated table must be listed (use-events.test.ts checks src/generated/replicated-tables.json).
export const STATE_FAMILIES: Record<string, string[][]> = {
  // Usage counters arrive through usage_hourly, written in the same transaction as each turn.
  sessions: [["m", LOCAL, "sessions"], ["m", LOCAL, "session-row"]],
  limits: [["m", LOCAL, "limits"]],
  usage_hourly: [["m", LOCAL, "usage"]],
  limit_samples: [["m", LOCAL, "usage"]],
  users: [["identity"]],
  node_settings: [["identity"], ["nodes"], ["topology"], ["settings"]],
  mesh_nodes: [["nodes"], ["topology"], ["replication"]],
  mesh_settings: [["nodes"], ["topology"], ["replication"], ["settings"]],
  issues: [["diagnostics"]],
  issue_counts: [["diagnostics"]],
  conversation_events: [],
  // Native chat ownership is read only by resume checks on the gateway.
  acp_session_owners: [],
  archive_blobs: [],
  archive_edits: [],
  archive_event_clocks: [],
  blob_locations: [],
  mcp_credentials: [["mcp-credentials"]],
  mcp_credential_approvals: [["mcp-credentials"]],
  agent_snapshots: [["agent-snapshot"]],
  agent_credentials: [["agents"]],
  registry_objects: [["m", LOCAL, "library"], ["drafts"]],
  registry_names: [["m", LOCAL, "library"], ["drafts"]],
  registry_heads: [["m", LOCAL, "library"], ["drafts"]],
  registry_tombstones: [["m", LOCAL, "library"], ["drafts"]],
  registry_drafts: [["m", LOCAL, "library"], ["drafts"]],
  cache_locations: [],
  omp_settings: [["omp-settings"]],
  // Web Push state feeds no query: this device reads its subscription from the browser.
  push_keys: [],
  push_subscriptions: [],
}
const ALL_STATE_FAMILIES = [...new Map(Object.values(STATE_FAMILIES).flat().map((key) => [key.join("\0"), key])).values()]

// Unknown tables, or an older gateway that sends none, refetch every family.
export function stateFamilies(tables: string[] | undefined): string[][] {
  if (!tables || tables.some((table) => !(table in STATE_FAMILIES))) return ALL_STATE_FAMILIES
  const keys = new Map<string, string[]>()
  for (const table of tables) for (const key of STATE_FAMILIES[table]!) keys.set(key.join("\0"), key)
  return [...keys.values()]
}

// A closed EventSource never reopens by itself; retries back off up to half a minute.
export function reconnectDelay(attempt: number): number {
  return Math.min(30_000, 1000 * 2 ** attempt)
}

// Events missed while disconnected: every replicated family plus each machine's live queries.
function catchUpAll(queryClient: QueryClient) {
  invalidate(queryClient, ["m"])
  for (const key of ALL_STATE_FAMILIES) if (key[0] !== "m") invalidate(queryClient, key)
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
  if (msg.topic === "diagnostics") return invalidate(queryClient, ["diagnostics"])
  if (msg.topic === "nodes") {
    invalidate(queryClient, ["nodes"])
    invalidate(queryClient, ["topology"])
    invalidate(queryClient, ["replication"])
    return
  }
  // Chat shells are sessions of the chat's machine; they live outside the sessions key.
  if (msg.topic === "sessions") invalidate(queryClient, ["m", msg.node || LOCAL, "shells"])
  // An open chat follows its own stream, so a sessions change never refetches conversations.
  invalidate(queryClient, ["m", msg.node || LOCAL, msg.topic])
}

type ConversationSubscription = {
  machine: string
  sessionId: string
  onEntry: (data: string) => void
  onTruncated: () => void
  // Entries after the last one shown were evicted before this stream sent them.
  onGap: () => void
  // The server's fold of the chat's log (internal/network conversationDerived); older owners send none.
  onDerived: (data: string) => void
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
  const subscribe = (subscription: ConversationSubscription) => {
    setConversation(subscription)
    return () => setConversation((current) => current === subscription ? undefined : current)
  }
  useEffect(() => {
    let source: EventSource | null = null
    let retry: ReturnType<typeof setTimeout> | undefined
    let attempt = 0
    let opened = false
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
        attempt = 0
        opened = true
        connection(true)
        if (catchUp.current) {
          catchUp.current = false
          catchUpAll(queryClient)
        }
      }
      source.onerror = () => {
        connection(false)
        catchUp.current = true
        // A chat stream reopens with a fresh `since`; a plain stream the browser gave up on reopens too.
        if (chat || source?.readyState === EventSource.CLOSED) {
          close()
          if (!document.hidden) {
            retry = setTimeout(open, reconnectDelay(attempt))
            attempt = attempt + 1
          }
        }
      }
      source.onmessage = (event: MessageEvent<string>) =>
        dispatch(queryClient, JSON.parse(event.data) as EventMessage)
      if (chat) {
        source.addEventListener("conversation", (event: MessageEvent<string>) => chat.onEntry(event.data))
        source.addEventListener("conversation_truncated", chat.onTruncated)
        source.addEventListener("conversation_gap", chat.onGap)
        source.addEventListener("conversation_derived", (event: MessageEvent<string>) => chat.onDerived(event.data))
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
      // The next stream (another chat, or none) starts after a gap.
      if (opened) catchUp.current = true
    }
  }, [conversation, multiplexed, queryClient])
  return (
    <EventsContext value={connected}>
      <ConversationEventsContext value={multiplexed ? subscribe : undefined}>{children}</ConversationEventsContext>
    </EventsContext>
  )
}
