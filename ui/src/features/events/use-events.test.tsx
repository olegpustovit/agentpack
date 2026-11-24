import { QueryClientProvider, useQuery, type QueryClient } from "@tanstack/react-query"
import { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { createQueryClient } from "@/lib/query-client"
import { LOCAL } from "@/lib/api"
import { configQuery } from "@/lib/config"
import replicated from "@/generated/replicated-tables.json"
import { EventsProvider, STATE_FAMILIES, stateFamilies, useConversationEvents, useEventsConnected, usePausedEvents } from "./use-events"

class FakeEventSource {
  static instances: FakeEventSource[] = []
  static CLOSED = 2
  readyState = 0
  closed = false
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((event: MessageEvent<string>) => void) | null = null
  addEventListener = vi.fn()
  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }
  close() {
    this.closed = true
  }
  emit(msg: unknown) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(msg) }))
  }
}

let calls: string[] = []
let connected = false
let hidden = false
let subscribeToChat: ReturnType<typeof useConversationEvents>
let paused: ReturnType<typeof usePausedEvents>

const tracked = (queryKey: string[]) => ({
  queryKey,
  queryFn: () => {
    calls.push(queryKey.join("/"))
    return 1
  },
})

function Probe() {
  connected = useEventsConnected()
  paused = usePausedEvents()
  subscribeToChat = useConversationEvents()
  useQuery(tracked(["m", "local", "sessions"]))
  useQuery(tracked(["m", "peer-1", "sessions"]))
  useQuery(tracked(["m", "local", "projects"]))
  useQuery(tracked(["m", "local", "shells", "s1"]))
  useQuery(tracked(["nodes"]))
  useQuery(tracked(["topology"]))
  useQuery(tracked(["diagnostics", "issues"]))
  return null
}

let container: HTMLDivElement
let client: QueryClient
let root: Root

const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0))

beforeEach(async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  vi.stubGlobal("EventSource", FakeEventSource)
  FakeEventSource.instances = []
  calls = []
  connected = false
  hidden = false
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden })
  client = createQueryClient()
  client.setQueryData(configQuery.queryKey, { ui_build_id: "test", conversation_events: true })
  client.setQueryData(["m", LOCAL, "sessions"], 0)
  container = document.createElement("div")
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <EventsProvider>
          <Probe />
        </EventsProvider>
      </QueryClientProvider>,
    )
    await flush()
  })
  calls = []
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  client.clear()
  vi.unstubAllGlobals()
})

describe("event stream", () => {
  it("refetches the local sessions key and its chat shells for a local sessions event", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => {
      source.emit({ topic: "sessions", node: "" })
      await flush()
    })
    expect(calls.sort()).toEqual(["m/local/sessions", "m/local/shells/s1"])
  })

  it("shares an in-flight sessions fetch between state and sessions events", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => {
      source.emit({ topic: "state", tables: ["sessions"] })
      source.emit({ topic: "sessions" })
    })
    expect(calls.sort()).toEqual(["m/local/sessions", "m/local/shells/s1"])
  })

  it("refetches unified sessions and the peer's key for a peer event", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => {
      source.emit({ topic: "sessions", node: "peer-1" })
      await flush()
    })
    expect(calls).toEqual(["m/local/sessions", "m/peer-1/sessions"])
  })

  it("refreshes unified state after a mesh sync", async () => {
    await act(async () => {
      FakeEventSource.instances[0]!.emit({ topic: "state", node: "peer-1" })
      await flush()
    })
    expect(calls.sort()).toEqual(["diagnostics/issues", "m/local/sessions", "nodes", "topology"])
  })

  it("invalidates only the affected peer and the local Library when registry state changes", async () => {
    const keys = [LOCAL, "peer-1", "peer-2"].map((machine) => ["m", machine, "library", "packages"])
    for (const key of keys) client.setQueryData(key, [])
    await act(async () => FakeEventSource.instances[0]!.emit({ topic: "state", node: "peer-1", tables: ["registry_objects", "registry_heads"] }))
    expect(keys.map((key) => client.getQueryState(key)?.isInvalidated)).toEqual([true, true, false])
  })

  it("refreshes the issue list for a diagnostics event", async () => {
    await act(async () => {
      FakeEventSource.instances[0]!.emit({ topic: "diagnostics" })
      await flush()
    })
    expect(calls).toEqual(["diagnostics/issues"])
  })

  it("refreshes nodes and topology for a nodes event", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => {
      source.emit({ topic: "nodes", node: "" })
      await flush()
    })
    expect(calls.sort()).toEqual(["nodes", "topology"])
  })

  it("catches up every live query after a reconnect, including per-machine ones", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => source.onopen?.())
    await act(async () => source.onerror?.())
    calls = []
    await act(async () => {
      source.onopen?.()
      await flush()
    })
    expect(calls.sort()).toEqual(["diagnostics/issues", "m/local/projects", "m/local/sessions", "m/local/shells/s1", "m/peer-1/sessions", "nodes", "topology"])
  })

  it("reports a paused stream and manually reconnects and reconciles once", async () => {
    const first = FakeEventSource.instances[0]!
    await act(async () => first.onopen?.())
    expect(paused).toBeNull()
    await act(async () => first.onerror?.())
    expect(paused?.lastReceivedAt).toBeGreaterThan(0)
    calls = []
    await act(async () => paused?.reconnect())
    expect(first.closed).toBe(true)
    expect(FakeEventSource.instances).toHaveLength(2)
    const second = FakeEventSource.instances[1]!
    await act(async () => second.onopen?.())
    expect(paused).toBeNull()
    expect(connected).toBe(true)
    await act(async () => {
      second.onopen?.()
      await flush()
    })
    expect(calls.filter((call) => call === "m/local/projects")).toHaveLength(1)
    expect(calls.filter((call) => call === "m/peer-1/sessions")).toHaveLength(1)
  })

  it("refreshes restored cache data on the first open", async () => {
    const key = ["m", LOCAL, "sessions"]
    expect(client.getQueryData(key)).toBe(0)
    await act(async () => FakeEventSource.instances[0]!.onopen?.())
    expect(client.getQueryData(key)).toBe(1)
    expect(calls).toContain("m/peer-1/sessions")
  })

  it("skips catch-up when a healthy stream reopens for a conversation switch", async () => {
    const follow = async (sessionId: string) => {
      await act(async () => {
        const noop = () => {}
        subscribeToChat!({
          machine: LOCAL, sessionId, onEntry: noop, onTruncated: noop,
          onGap: noop, onDerived: noop, onConnection: noop, onFallback: noop,
        })
      })
      const source = FakeEventSource.instances.at(-1)!
      expect(new URL(source.url, location.origin).searchParams.get("session")).toBe(sessionId)
      await act(async () => source.onopen?.())
      return source
    }

    const first = await follow("first")
    calls = []
    await follow("second")
    expect(first.closed).toBe(true)
    expect(calls).toEqual([])
  })

  it("reopens a stream the browser closed, backing off", async () => {
    vi.useFakeTimers()
    try {
      const first = FakeEventSource.instances[0]!
      first.readyState = FakeEventSource.CLOSED
      act(() => first.onerror?.())
      expect(first.closed).toBe(true)
      act(() => vi.advanceTimersByTime(999))
      expect(FakeEventSource.instances).toHaveLength(1)
      act(() => vi.advanceTimersByTime(1))
      const second = FakeEventSource.instances[1]!
      second.readyState = FakeEventSource.CLOSED
      act(() => second.onerror?.())
      act(() => vi.advanceTimersByTime(1999))
      expect(FakeEventSource.instances).toHaveLength(2)
      act(() => vi.advanceTimersByTime(1))
      expect(FakeEventSource.instances).toHaveLength(3)
    } finally {
      vi.useRealTimers()
    }
  })

  it("reports connected after open and closes while the tab is hidden", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => source.onopen?.())
    expect(connected).toBe(true)

    hidden = true
    await act(async () => document.dispatchEvent(new Event("visibilitychange")))
    expect(source.closed).toBe(true)
    expect(connected).toBe(false)

    hidden = false
    await act(async () => document.dispatchEvent(new Event("visibilitychange")))
    expect(FakeEventSource.instances).toHaveLength(2)
  })
})

describe("stateFamilies", () => {
  it("refetches nothing for chat writes, only the readers of known tables, and everything otherwise", () => {
    expect(stateFamilies(["conversation_events"])).toEqual([])
    expect(stateFamilies(["sessions", "conversation_events"])).toEqual([["m", LOCAL, "sessions"], ["m", LOCAL, "session-row"]])
    expect(stateFamilies(["mesh_nodes", "mesh_settings"])).toHaveLength(4)
    expect(stateFamilies(["mcp_credentials", "mcp_credential_approvals"])).toEqual([["mcp-credentials"]])
    expect(stateFamilies(["agent_snapshots"])).toEqual([["agent-snapshot"]])
    expect(stateFamilies(["omp_settings"])).toEqual([["omp-settings"]])
    expect(stateFamilies(["agent_credentials"])).toEqual([["agents"]])
    expect(stateFamilies(["registry_heads"])).toEqual([["m", LOCAL, "library"], ["drafts"]])
    expect(stateFamilies(["node_settings"])).toContainEqual(["settings"])
    expect(stateFamilies(undefined)).toHaveLength(16)
    expect(stateFamilies(["a_future_table"])).toHaveLength(16)
  })

  // internal/state TestReplicatedTablesMatchUIList keeps the JSON in step with the schema.
  it("maps exactly the tables the gateway replicates", () => {
    expect(Object.keys(STATE_FAMILIES).sort()).toEqual(replicated)
  })
})
