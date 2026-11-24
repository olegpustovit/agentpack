import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react"
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query"
import { LOCAL } from "@/lib/api"
import type { TailnetNode } from "@/lib/api-types"
import { configQuery } from "@/lib/config"
import { hud } from "@/lib/hud"
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
async function catchUpAll(queryClient: QueryClient, recoveryStillCurrent?: () => boolean) {
  const reads = recoveryStillCurrent
    ? queryClient.getQueryCache().findAll({ queryKey: ["m"], type: "active" }).map((query) => ({
        query, updates: query.state.dataUpdateCount,
      }))
    : []
  await Promise.all([invalidate(queryClient, ["m"]), ...ALL_STATE_FAMILIES.filter((key) => key[0] !== "m").map((key) => invalidate(queryClient, key))])
  if (!recoveryStillCurrent?.()) return
  const peers = new Set(reads.filter(({ query, updates }) => query.state.dataUpdateCount > updates).map(({ query }) => query.queryKey[1]))
  const nodes = queryClient.getQueryData<TailnetNode[]>(["nodes"])
  const refreshed = Array.isArray(nodes) ? nodes.filter((node) => peers.has(node.id)).map((node) => node.name) : []
  hud.success(refreshed.length ? `Live updates resumed · Refreshed ${refreshed.join(", ")}` : "Live updates resumed")
}

const EventsContext = createContext(false)
const PausedEventsContext = createContext<{ lastReceivedAt?: number; reconnect: () => void } | null>(null)

// True while the /api/events stream is open. Polling queries read it to stop their timers.
export function useEventsConnected(): boolean {
  return useContext(EventsContext)
}

export function usePausedEvents() {
  return useContext(PausedEventsContext)
}

function invalidate(queryClient: QueryClient, queryKey: string[]) {
  return queryClient.invalidateQueries({ queryKey, refetchType: "active" }, { cancelRefetch: false })
}

function dispatch(queryClient: QueryClient, msg: EventMessage) {
  if (msg.topic === "state") {
    for (const key of stateFamilies(msg.tables)) {
      invalidate(queryClient, key)
      if (msg.node && msg.node !== LOCAL && key[0] === "m" && key[2] === "library") {
        invalidate(queryClient, ["m", msg.node, "library"])
      }
    }
    return
  }
  if (msg.node && msg.node !== LOCAL && (msg.topic === "sessions" || msg.topic === "limits")) {
    invalidate(queryClient, ["m", LOCAL, msg.topic])
  }
  if (msg.topic === "replication") {
    invalidate(queryClient, ["replication"])
    invalidate(queryClient, ["m", LOCAL, "usage"])
  }
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
  const [paused, setPaused] = useState<{ lastReceivedAt?: number }>()
  const [reconnectRequest, setReconnectRequest] = useState(0)
  const [conversation, setConversation] = useState<ConversationSubscription>()
  const catchUp = useRef(true)
  const interrupted = useRef(false)
  const lastReceivedAt = useRef<number | undefined>(undefined)
  const subscribe = (subscription: ConversationSubscription) => {
    setConversation(subscription)
    return () => setConversation((current) => current === subscription ? undefined : current)
  }
  useEffect(() => {
    let source: EventSource | null = null
    let retry: ReturnType<typeof setTimeout> | undefined
    let attempt = 0
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
      const stream = new EventSource(path)
      source = stream
      stream.onopen = () => {
        if (source !== stream) return
        attempt = 0
        lastReceivedAt.current = Date.now()
        setPaused(undefined)
        connection(true)
        if (catchUp.current) {
          catchUp.current = false
          const recovering = interrupted.current
          interrupted.current = false
          void catchUpAll(queryClient, recovering ? () => source === stream && !interrupted.current : undefined)
        }
      }
      stream.onerror = () => {
        if (source !== stream) return
        connection(false)
        catchUp.current = true
        interrupted.current = true
        const last = lastReceivedAt.current
        setPaused((current) => current ?? { lastReceivedAt: last })
        // A chat stream reopens with a fresh `since`; a plain stream the browser gave up on reopens too.
        if (chat || stream.readyState === EventSource.CLOSED) {
          close()
          if (!document.hidden) {
            retry = setTimeout(open, reconnectDelay(attempt))
            attempt = attempt + 1
          }
        }
      }
      stream.onmessage = (event: MessageEvent<string>) => {
        if (source !== stream) return
        lastReceivedAt.current = Date.now()
        dispatch(queryClient, JSON.parse(event.data) as EventMessage)
      }
      if (chat) {
        stream.addEventListener("conversation", (event: MessageEvent<string>) => {
          if (source !== stream) return
          lastReceivedAt.current = Date.now()
          chat.onEntry(event.data)
        })
        stream.addEventListener("conversation_truncated", chat.onTruncated)
        stream.addEventListener("conversation_gap", chat.onGap)
        stream.addEventListener("conversation_derived", (event: MessageEvent<string>) => chat.onDerived(event.data))
        stream.addEventListener("conversation_fallback", chat.onFallback)
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
  }, [conversation, multiplexed, queryClient, reconnectRequest])
  return (
    <EventsContext value={connected}>
      <PausedEventsContext value={paused ? { ...paused, reconnect: () => setReconnectRequest((n) => n + 1) } : null}>
        <ConversationEventsContext value={multiplexed ? subscribe : undefined}>{children}</ConversationEventsContext>
      </PausedEventsContext>
    </EventsContext>
  )
}
