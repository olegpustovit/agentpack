import { QueryClientProvider, queryOptions, type QueryClient } from "@tanstack/react-query"
import { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { ApiError, api } from "@/lib/api"
import { createQueryClient } from "@/lib/query-client"
import { sessionsQuery } from "@/features/sessions/queries"
import { useMachineQueries, useReplicatedQueries, partitionReplicatedRows, type MachineQueries } from "./queries"

type Json = unknown

function json(body: Json, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
}

const identity = { node: { id: "self", name: "this-mac", os: "darwin", version: "1" }, auth: "", tailscale_user: "" }

let routes: Record<string, (url: URL) => Promise<Response> | Response>

const perMachineTestQuery = (machine: string) => queryOptions({queryKey: ["m", machine, "sessions"], queryFn: () => api<unknown[]>("/api/sessions", {machine})})

function Probe({ out }: { out: { current?: MachineQueries<unknown> } }) {
  out.current = useMachineQueries(perMachineTestQuery)
  return null
}

let container: HTMLDivElement
let client: QueryClient
let root: Root

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement("div")
  document.body.appendChild(container)
  client = createQueryClient()
  root = createRoot(container)
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost")
      const key = url.pathname + (url.searchParams.get("node") ? `?node=${url.searchParams.get("node")}` : "")
      const handler = routes[key]
      if (!handler) return Promise.resolve(json({ error: "no route " + key }, 404))
      return Promise.resolve(handler(url))
    }),
  )
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  client.clear()
  vi.unstubAllGlobals()
})

async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

describe("useMachineQueries", () => {
  it("shows a fast machine while a slow machine is still pending", async () => {
    routes = {
      "/api/identity": () => json(identity),
      "/api/nodes": () => json([{ id: "fast", name: "fast", online: true }, { id: "slow", name: "slow", online: true }]),
      "/api/sessions?node=slow": () => new Promise(() => {}),
      "/api/sessions?node=fast": () => json([]),
    }
    const out: { current?: MachineQueries<unknown> } = {}
    await act(async () => root.render(<QueryClientProvider client={client}><Probe out={out} /></QueryClientProvider>))
    await flush()
    await flush()

    expect(out.current?.byMachine.fast?.data).toEqual([])
    expect(out.current?.byMachine.slow?.pending).toBe(true)
    expect(out.current?.firstLoadPending).toBe(true)
  })

  it("keeps cached data and exposes the error when a refetch fails", async () => {
    let failing = false
    routes = {
      "/api/identity": () => json(identity),
      "/api/nodes": () => json([{ id: "fast", name: "fast", online: true }]),
      "/api/sessions?node=fast": () => (failing ? json({ error: "boom" }, 400) : json([{ id: "s1" }])),
    }
    const out: { current?: MachineQueries<unknown> } = {}
    await act(async () => root.render(<QueryClientProvider client={client}><Probe out={out} /></QueryClientProvider>))
    await flush()
    await flush()
    expect(out.current?.byMachine.fast?.data).toEqual([{ id: "s1" }])

    failing = true
    await act(async () => {
      await client.refetchQueries({ queryKey: ["m", "fast", "sessions"] })
    })
    await flush()

    expect(out.current?.byMachine.fast?.data).toEqual([{ id: "s1" }])
    const error = out.current?.byMachine.fast?.error
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).status).toBe(400)
    expect(out.current?.byMachine.fast?.needsUpdate).toBe(false)
  })
})

function ReplicatedProbe({ out }: { out: { current?: MachineQueries<unknown> } }) {
  out.current = useReplicatedQueries(sessionsQuery)
  return null
}

it("loads offline peer sessions in one local request", async () => {
  routes = {
    "/api/identity": () => json(identity),
    "/api/nodes": () => json([{id:"asleep",name:"Sleeping computer",online:false}]),
    "/api/sessions": () => json([{id:"local-session",node_id:"self",reachable:true},{id:"remote-session",node_id:"asleep",reachable:false}]),
  }
  const out: { current?: MachineQueries<unknown> } = {}
  await act(async () => root.render(<QueryClientProvider client={client}><ReplicatedProbe out={out} /></QueryClientProvider>))
  await flush(); await flush()
  expect(out.current?.byMachine.local?.data).toEqual([{id:"local-session",node_id:"self",reachable:true}])
  expect(out.current?.byMachine.asleep?.data).toEqual([{id:"remote-session",node_id:"asleep",reachable:false}])
  const calls = vi.mocked(fetch).mock.calls.map(([input]) => { const url = new URL(String(input)); return url.pathname + url.search })
  expect(calls.filter((url) => url.includes("/api/sessions"))).toEqual(["/api/sessions"])
})

it("partitions by the source node and keeps legacy local rows", () => {
  expect(partitionReplicatedRows([{node_id:"self"},{node_id:"peer"},{}],"self")).toEqual({local:[{node_id:"self"},{}],peer:[{node_id:"peer"}]})
})
