import { expect, type Page, type WebSocketRoute } from "@playwright/test"

// Shared by the gateway specs. Later page.route() calls in a test override these defaults.
export const identity = { node: { id: "gateway", name: "Gateway machine", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }

const launched = "2026-10-26T10:30:00Z"
const started = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()

export const session = {
  id: "local-one",
  directory: "/work/my-project",
  harness: "claude",
  mode: "default",
  state: { interactive: true, running: true, restarting: false, shared_input: true, responder: 42, rows: 24, columns: 80, exit_code: 0 },
  started_at: started,
  last_used: started,
}
export const second = { ...session, id: "local-two", directory: "/work/another-project", harness: "codex" }

// Recent folder as /api/projects returns it. Each launch is one harness that ran in the folder.
export function projectRow(directory: string, launches: { harness: string; mode?: string }[] = [{ harness: "codex" }], overrides: Record<string, unknown> = {}) {
  return {
    directory,
    name: directory.split("/").pop(),
    folder_id: `folder-${directory}`,
    available: true,
    unavailable_reason: "",
    last_used: launched,
    launches: launches.map((launch) => ({ harness: launch.harness, mode: launch.mode ?? "default", last_used: launched })),
    packages: [],
    mcp: [],
    error: "",
    ...overrides,
  }
}

// Audit fixture: 3 machines (this one plus two peers), 4 projects each, 3 harnesses per project.
export const fleetNodes = [
  { id: "peer-1", name: "Build server", online: true },
  { id: "peer-2", name: "oracle-vm", online: true },
]
export const fleetHarnesses = ["claude", "codex", "cursor"]
const fleetProjectNames = ["alpha", "beta", "gamma", "delta"]

function fleetRoot(machine: string) {
  return machine === "local" ? "/work" : "/srv"
}

export function fleetProjects(machine: string) {
  return fleetProjectNames.map((name) => projectRow(`${fleetRoot(machine)}/${name}`, fleetHarnesses.map((harness) => ({ harness }))))
}

export function fleetSessions(machine: string) {
  return fleetProjectNames.flatMap((name) =>
    fleetHarnesses.map((harness) => ({ ...session, id: `${machine}-${name}-${harness}`, directory: `${fleetRoot(machine)}/${name}`, harness, state: { ...session.state, shared_input: false } })),
  )
}

// Legacy mockAPI shape: routes the API, records every request, and returns the record.
// `fleet` switches the list endpoints to the audit fixture; `node` selects the machine in each reply.
export async function mockAPI(page: Page, { authenticated = true, setup = false, fleet = false }: { authenticated?: boolean; setup?: boolean; fleet?: boolean } = {}) {
  const requests: { method: string; path: string; query: URLSearchParams; body: unknown }[] = []
  let loggedIn = authenticated
  let setupNeeded = setup
  await page.route("**/api/**", async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const body = req.method() === "GET" ? undefined : req.postDataJSON()
    requests.push({ method: req.method(), path: url.pathname, query: url.searchParams, body })
    const machine = url.searchParams.get("node") ?? "local"
    let data: unknown = {}
    if (url.pathname === "/api/status") data = { setup_required: setupNeeded, authenticated: loggedIn }
    else if (url.pathname === "/api/login") loggedIn = true
    else if (url.pathname === "/api/setup") {
      expect(body).toMatchObject({ token: "private-token", password: "a-long-test-password" })
      setupNeeded = false
      loggedIn = true
    } else if (url.pathname === "/api/identity") data = identity
    else if (url.pathname === "/api/nodes") data = fleet ? fleetNodes : []
    else if (url.pathname === "/api/library/drafts") data = []
    else if (url.pathname === "/api/library/replication/peers") data = { peers: [], contacts: {} }
    else if (url.pathname === "/api/events") return route.abort()
    else if (url.pathname === "/api/sessions") {
      const list = fleet ? fleetSessions(machine) : [session, second]
      data = req.method() === "POST" ? { ...session, ...body, id: "created" } : list
    } else if (url.pathname === "/api/projects") data = fleet ? fleetProjects(machine) : [projectRow("/work/previous", [{ harness: "codex" }])]
    else if (url.pathname === "/api/packages") data = req.method() === "POST" ? {} : [{ module: "github.com/acme/tools", commit: "abcd1234" }]
    else if (url.pathname === "/api/settings") data = { running: true, platform: "linux", sharing_enabled: true, allowed_peers: ["tag:server"] }
    else if (url.pathname === "/api/settings/sharing") data = { sharing_enabled: body?.enabled }
    else if (url.pathname === "/api/topology") data = { self: { id: "gateway", name: "Gateway machine", os: "linux", relay: "" }, peers: [] }
    else if (url.pathname === "/api/library/packages") data = []
    else if (url.pathname === "/api/library/agentpacks") data = []
    else if (url.pathname === "/api/limits") {
      data = { limits: [{ harness: "codex", state: "available", source: "codex app-server account/rateLimits/read", observed_at: new Date().toISOString(), windows: [{ id: "codex/primary", label: "Codex 5h", used_percent: 28, remaining_percent: 72, reset_at: "2026-10-30T16:10:00Z" }] }, { harness: "grok", state: "error", source: "grok agent stdio _x.ai/billing", observed_at: new Date().toISOString(), error: "grok billing: authentication required", windows: [] }] }
    }
    await route.fulfill({ json: data })
  })
  return requests
}

// Legacy attachTerminal shape: answers every terminal socket and records the client's messages.
export async function attachTerminal(page: Page, { terminalState = {}, holdReplay = false, initialQuery = "" }: { terminalState?: Record<string, unknown>; holdReplay?: boolean; initialQuery?: string } = {}) {
  const sockets: WebSocketRoute[] = []
  const controls = Object.assign<(string | Buffer)[], { sockets: WebSocketRoute[] }>([], { sockets })
  await page.routeWebSocket("**/api/sessions/*/terminal*", (ws) => {
    sockets.push(ws)
    ws.send(JSON.stringify({ type: "attached", attachment: 42, state: { ...session.state, shared_input: true, responder: 42, ...terminalState } }))
    ws.send(Buffer.from(`READY\r\n${initialQuery}`))
    if (!holdReplay && terminalState.shared_input !== false) ws.send(JSON.stringify({ type: "replay_complete" }))
    ws.onMessage((message) => {
      controls.push(message)
    })
  })
  return controls
}
