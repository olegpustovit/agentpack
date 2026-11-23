import { afterEach, beforeEach, expect, it, vi } from "vitest"

beforeEach(() => {
  vi.resetModules()
  vi.useFakeTimers()
  vi.setSystemTime(60_000)
  vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 204 })))
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function batches() {
  return vi.mocked(fetch).mock.calls.flatMap(([, options]) => JSON.parse(String(options?.body)).events)
}

it("keeps only the last 30 breadcrumbs and correlates the last API trace", async () => {
  const { breadcrumb, traceRequest, recordAPI, reportError, flushTelemetry } = await import("./telemetry")
  for (let n = 0; n < 40; n++) breadcrumb({ kind: "click", name: String(n) })
  const trace = traceRequest()
  recordAPI("GET", "/api/sessions?secret=private", trace, 200)
  reportError(new Error("component failed"))
  flushTelemetry()
  const [event] = batches()
  expect(event.breadcrumbs).toHaveLength(30)
  expect(event.breadcrumbs[0].name).toBe("11")
  expect(event.breadcrumbs.at(-1)).toMatchObject({ kind: "api", name: "GET /api/sessions", trace_id: trace.slice(3, 35), status: 200 })
  expect(event.traceparent).toBe(trace)
  expect(JSON.stringify(event)).not.toContain("private")
})

it("deduplicates ten seconds, limits twenty per minute and batches flushes", async () => {
  const { reportError, flushTelemetry } = await import("./telemetry")
  reportError(new Error("same failure"))
  reportError(new Error("same failure"))
  await vi.advanceTimersByTimeAsync(200)
  expect(batches()).toHaveLength(1)
  await vi.advanceTimersByTimeAsync(9800)
  reportError(new Error("same failure"))
  for (let n = 0; n < 30; n++) reportError(new Error(`failure ${n}`))
  flushTelemetry()
  expect(batches()).toHaveLength(20)
  expect(vi.mocked(fetch).mock.calls.every(([, options]) => JSON.parse(String(options?.body)).events.length <= 5)).toBe(true)
  await vi.advanceTimersByTimeAsync(50_000)
  reportError(new Error("new minute"))
  flushTelemetry()
  expect(batches()).toHaveLength(21)
})

it("reports only network failures and server errors with no response text", async () => {
  const { api } = await import("./api")
  const { reportError } = await import("./telemetry")
  for (const status of [200, 400, 401, 404, 409, 429, 500, 502]) {
    vi.mocked(fetch).mockResolvedValueOnce(new Response("private response", { status }))
    await api(`/api/status-${status}`).catch(reportError)
  }
  vi.mocked(fetch).mockRejectedValueOnce(new Error("private network details"))
  await api("/api/offline").catch(reportError)
  vi.mocked(fetch).mockResolvedValueOnce(Object.assign(new Response(null), { text: async () => { throw new Error("private partial response") } }))
  await api("/api/interrupted").catch(reportError)
  const { flushTelemetry } = await import("./telemetry")
  vi.mocked(fetch).mockClear()
  flushTelemetry()
  expect(batches()).toHaveLength(4)
  expect(batches().map((event) => event.message)).toEqual([
    "API GET /api/status-500 (500)", "API GET /api/status-502 (502)", "API GET /api/offline (0)", "API GET /api/interrupted (0)",
  ])
  expect(JSON.stringify(batches())).not.toContain("private")
})

it("uses beacon on page hide and does not recurse when uploads fail", async () => {
  const { reportError, flushTelemetry } = await import("./telemetry")
  const beacon = vi.fn(() => true)
  vi.stubGlobal("navigator", { sendBeacon: beacon })
  reportError(new Error("leaving"))
  flushTelemetry(true)
  expect(beacon).toHaveBeenCalledOnce()
  expect(fetch).not.toHaveBeenCalled()
  vi.mocked(fetch).mockRejectedValue(new Error("offline"))
  reportError(new Error("upload failure"))
  flushTelemetry()
  await vi.advanceTimersByTimeAsync(60_000)
  expect(fetch).toHaveBeenCalledOnce()
})

it("records named controls and safe route context without form or chat contents", async () => {
  const { installTelemetry, reportError, flushTelemetry } = await import("./telemetry")
  installTelemetry(() => ({ route: "/sessions/$machine/$sessionId", ui_build_id: "EntryHash", search: { view: "active", prompt: "private prompt", token: "private token", q: "private query" } }))
  document.body.innerHTML = '<button aria-label="Refresh">icon</button><span id="label">Retry</span><button aria-labelledby="label">icon</button><input value="private input"><div data-slot="agent-message"><a aria-label="private reply" href="#">private chat</a></div>'
  for (const control of document.querySelectorAll<HTMLElement>("button,input,a")) control.click()
  reportError(new Error("render failed"))
  flushTelemetry()
  const [event] = batches()
  expect(event.route).toBe("/sessions/$machine/$sessionId")
  expect(event.ui_build_id).toBe("EntryHash")
  expect(event.search).toEqual({ view: "active" })
  expect(event.breadcrumbs.map((crumb: { name: string }) => crumb.name)).toEqual(["Refresh", "Retry"])
  expect(JSON.stringify(event)).not.toContain("private")
  document.body.innerHTML = ""
})

it("splits multibyte bursts below the server's byte limit without dropping errors", async () => {
  const { breadcrumb, reportError, flushTelemetry } = await import("./telemetry")
  for (let n = 0; n < 30; n++) breadcrumb({ kind: "click", name: "日".repeat(128) })
  for (let n = 0; n < 5; n++) {
    const error = new Error(`${n}${"日".repeat(1024)}`)
    error.stack = "日".repeat(2048)
    reportError(error)
  }
  flushTelemetry()
  expect(batches()).toHaveLength(5)
  expect(vi.mocked(fetch).mock.calls.length).toBeGreaterThan(1)
  for (const [, options] of vi.mocked(fetch).mock.calls) expect(new Blob([String(options?.body)]).size).toBeLessThanOrEqual(64 << 10)
})

it("reports unexpected query and mutation errors once, skipping sign-in, older peers and aborts", async () => {
  const { ApiError } = await import("./api")
  const { createQueryClient } = await import("./query-client")
  const { flushTelemetry } = await import("./telemetry")
  const client = createQueryClient()
  const fail = (key: string, error: unknown) => client.fetchQuery({ queryKey: [key], queryFn: () => Promise.reject(error), retry: false }).catch(() => {})
  await fail("signed-out", new ApiError("unauthorized", 401))
  await fail("old-peer", new ApiError("not found", 404))
  await fail("aborted", new DOMException("The operation was aborted.", "AbortError"))
  await fail("shape", new Error("unexpected sessions in gateway response"))
  await fail("shape-again", new Error("unexpected sessions in gateway response"))
  const pending = client.fetchQuery({ queryKey: ["slow"], queryFn: () => new Promise(() => {}) }).catch(() => {})
  await client.cancelQueries({ queryKey: ["slow"] })
  await pending
  await client.getMutationCache().build(client, { mutationFn: () => Promise.reject(new TypeError("bad mutation input")) }).execute(undefined).catch(() => {})
  flushTelemetry()
  expect(batches().map((event) => event.message)).toEqual(["unexpected sessions in gateway response", "bad mutation input"])
})
