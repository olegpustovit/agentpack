import { expect, type Page, type WebSocketRoute } from "@playwright/test"

// Test-only hooks the app does not read; the specs set and read them through page.evaluate.
declare global {
  interface Window {
    __first?: Window["__agentpackTerminal"]
    __writes?: number
    __arrivals?: number
    __clip?: string
    __copied?: string | null
  }
}

export const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
export const shared = { interactive: true, running: true, restarting: false, shared_input: true, responder: 1, rows: 30, columns: 100, exit_code: 0 }
const startedAt = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()

export type Listed = { id: string; name: string; state: unknown; [key: string]: unknown }

export function session(id: string, name: string, overrides: Record<string, unknown> = {}): Listed {
  return { id, name, directory: "/home/oleg/agentpack", harness: "claude", mode: "default", state: shared, started_at: startedAt, last_used: startedAt, ...overrides }
}

// Row r holds 100 copies of `letter`, so any 40-cell run inside a row selects 40 characters.
export function screen(letter: string, { mouseModes = false }: { mouseModes?: boolean } = {}): Buffer {
  const rows = Array.from({ length: 30 }, (_, row) => `\x1b[${row + 1};1H` + letter.repeat(100)).join("")
  return Buffer.from((mouseModes ? "\x1b[?1000h\x1b[?1006h" : "") + "\x1b[2J" + rows)
}

// `count` lines of plain text, for scrollback tests.
export function history(count: number): Buffer {
  return Buffer.from(Array.from({ length: count }, (_, index) => `history ${index}\r\n`).join(""))
}

export type Conn = { session: string; index: number; since: string | null; ws: WebSocketRoute; closed: boolean; sent: string[] }

// Routes every terminal socket. `behave` decides what each new connection does; the default replays
// the "A" screen and marks the session shared.
export async function stubWorker(page: Page) {
  const conns: Conn[] = []
  let behave: (conn: Conn) => void = (conn) => serve(conn, { body: screen("A") })
  await page.routeWebSocket(/\/api\/sessions\/[^/]+\/terminal/, (ws) => {
    const url = new URL(ws.url())
    const conn: Conn = { session: url.pathname.split("/")[3] ?? "", index: conns.length + 1, since: url.searchParams.get("since"), ws, closed: false, sent: [] }
    conns.push(conn)
    ws.onMessage((message) => conn.sent.push(typeof message === "string" ? message : message.toString()))
    ws.onClose(() => {
      conn.closed = true
    })
    behave(conn)
  })
  return {
    conns,
    behave: (next: (conn: Conn) => void) => {
      behave = next
    },
  }
}

// What the worker sends on attach: attached (with the offset it resumed at), optional output, and replay_complete.
export function serve(conn: Conn, { offset = 0, body, complete = true }: { offset?: number; body?: Buffer; complete?: boolean } = {}) {
  const state = { ...shared, responder: conn.index }
  conn.ws.send(JSON.stringify({ type: "attached", attachment: conn.index, state, offset }))
  if (body) conn.ws.send(body)
  if (complete) conn.ws.send(JSON.stringify({ type: "replay_complete", attachment: conn.index, state }))
}

// Sessions list and the account endpoints. `sessions` is read on every list request.
export async function stubApi(page: Page, sessions: () => unknown[], nodes: unknown[] = []) {
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: nodes }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: sessions() }))
}

export function statusBar(page: Page) {
  return page.locator('[data-slot="status-bar"]')
}

// The stage is aria-busy while the socket is connecting, replaying, or reconnecting.
export function busyStage(page: Page) {
  return page.locator('[aria-busy="true"]')
}

export async function openRoute(page: Page, path: string) {
  await page.goto(path)
  await expect(statusBar(page)).toContainText("Shared input")
  // "Shared input" shows as soon as the attach state arrives; the replay is done when the stage is idle.
  await expect(busyStage(page)).toHaveCount(0)
}

// In-app navigation keeps the pool alive; a full page load would reset it.
export async function openFromList(page: Page, name: string) {
  await page.getByRole("link", { name: new RegExp(`${name}$`) }).click()
}

export async function rowText(page: Page, row: number): Promise<string> {
  return page.evaluate((index) => window.__agentpackTerminal?.buffer.active.getLine(index)?.translateToString(true) ?? "", row)
}

// Center of a grid cell on screen, from the rendered screen element and the grid size.
export async function cell(page: Page, x: number, y: number) {
  const box = await page.locator(".xterm-screen").boundingBox()
  if (!box) throw new Error("terminal screen is not rendered")
  const { cols, rows } = await page.evaluate(() => ({ cols: window.__agentpackTerminal?.cols ?? 0, rows: window.__agentpackTerminal?.rows ?? 0 }))
  return { x: box.x + ((x + 0.5) * box.width) / cols, y: box.y + ((y + 0.5) * box.height) / rows }
}

export async function dragCells(page: Page, from: { x: number; y: number }, to: { x: number; y: number }) {
  const start = await cell(page, from.x, from.y)
  const end = await cell(page, to.x, to.y)
  await page.mouse.move(start.x, start.y)
  await page.mouse.down()
  await page.mouse.move(end.x, end.y, { steps: 8 })
  await page.mouse.up()
}
