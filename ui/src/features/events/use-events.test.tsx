import { QueryClientProvider, useQuery, type QueryClient } from "@tanstack/react-query"
import { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { createQueryClient } from "@/lib/query-client"
import { EventsProvider, useEventsConnected } from "./use-events"

class FakeEventSource {
  static instances: FakeEventSource[] = []
  closed = false
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((event: MessageEvent<string>) => void) | null = null
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

const tracked = (queryKey: string[]) => ({
  queryKey,
  queryFn: () => {
    calls.push(queryKey.join("/"))
    return 1
  },
})

function Probe() {
  connected = useEventsConnected()
  useQuery(tracked(["m", "local", "sessions"]))
  useQuery(tracked(["m", "peer-1", "sessions"]))
  useQuery(tracked(["m", "local", "projects"]))
  useQuery(tracked(["nodes"]))
  useQuery(tracked(["topology"]))
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
  it("refetches only the local sessions key for a local sessions event", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => {
      source.emit({ topic: "sessions", node: "" })
      await flush()
    })
    expect(calls).toEqual(["m/local/sessions"])
  })

  it("refetches the peer's key for a peer event", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => {
      source.emit({ topic: "sessions", node: "peer-1" })
      await flush()
    })
    expect(calls).toEqual(["m/peer-1/sessions"])
  })

  it("refreshes nodes and topology for a nodes event", async () => {
    const source = FakeEventSource.instances[0]!
    await act(async () => {
      source.emit({ topic: "nodes", node: "" })
      await flush()
    })
    expect(calls.sort()).toEqual(["nodes", "topology"])
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
