import { expect, test, type Page } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
const state = { interactive: true, running: true, restarting: false, shared_input: true, responder: 1, rows: 30, columns: 100, exit_code: 0 }
const startedAt = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
const sessions = [{ id: "s1", name: "build", directory: "/home/oleg/agentpack", harness: "claude", mode: "default", state, started_at: startedAt, last_used: startedAt }]

const viewports = [
  { width: 1440, height: 900 },
  { width: 1100, height: 560 },
  { width: 800, height: 400 },
]

// Row r holds 100 letters, so any 40-cell run inside a row selects 40 characters.
function replay(mouseModes: boolean): Buffer {
  const rows = Array.from({ length: 30 }, (_, row) => `\x1b[${row + 1};1H` + String.fromCharCode(65 + (row % 26)).repeat(100)).join("")
  return Buffer.from((mouseModes ? "\x1b[?1000h\x1b[?1006h" : "") + "\x1b[2J" + rows)
}

// Stub worker: attaches as the responder with shared input, replays `output` (30 rows by default), and records what the browser sends.
async function openSession(page: Page, { mouseModes = false, sent = [] as string[], output = replay(mouseModes) } = {}) {
  await page.routeWebSocket(/\/api\/sessions\/s1\/terminal$/, (ws) => {
    ws.send(JSON.stringify({ type: "attached", attachment: 1, state, offset: 0 }))
    ws.send(output)
    ws.send(JSON.stringify({ type: "replay_complete", attachment: 1, state }))
    ws.onMessage((message) => sent.push(typeof message === "string" ? message : message.toString()))
  })
  await page.goto("/sessions/local/s1")
  await expect(page.locator('[data-slot="status-bar"]')).toContainText("Shared input")
}

// Center of a grid cell on screen, from the rendered screen element and the grid size.
async function cell(page: Page, x: number, y: number) {
  const box = await page.locator(".xterm-screen").boundingBox()
  if (!box) throw new Error("terminal screen is not rendered")
  const { cols, rows } = await page.evaluate(() => ({ cols: window.__agentpackTerminal?.cols ?? 0, rows: window.__agentpackTerminal?.rows ?? 0 }))
  return { x: box.x + ((x + 0.5) * box.width) / cols, y: box.y + ((y + 0.5) * box.height) / rows }
}

async function dragCells(page: Page, from: { x: number; y: number }, to: { x: number; y: number }) {
  const start = await cell(page, from.x, from.y)
  const end = await cell(page, to.x, to.y)
  await page.mouse.move(start.x, start.y)
  await page.mouse.down()
  await page.mouse.move(end.x, end.y, { steps: 8 })
  await page.mouse.up()
}

test.beforeEach(async ({ page }) => {
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: sessions }))
})

for (const viewport of viewports) {
  const size = `${viewport.width}×${viewport.height}`

  test(`drag from cell (20,20) to (60,20) selects 40 cells at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await openSession(page)
    await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
    expect(await page.evaluate(() => window.__agentpackTerminal?.getSelectionPosition())).toEqual({ start: { x: 20, y: 20 }, end: { x: 60, y: 20 } })
    expect(await page.evaluate(() => window.__agentpackTerminal?.getSelection().length)).toBe(40)
  })

  test(`a click with app mouse tracking sends the SGR press for cell (60,20) at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const sent: string[] = []
    await openSession(page, { mouseModes: true, sent })
    const point = await cell(page, 60, 20)
    await page.mouse.click(point.x, point.y)
    await expect.poll(() => sent).toContain("\x1b[<0;61;21M")
  })

  test(`shift-drag selects while the app owns the mouse at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const sent: string[] = []
    await openSession(page, { mouseModes: true, sent })
    await page.keyboard.down("Shift")
    await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
    await page.keyboard.up("Shift")
    expect(await page.evaluate(() => window.__agentpackTerminal?.getSelection().length)).toBe(40)
    expect(sent.some((message) => message.startsWith("\x1b[<"))).toBe(false)
  })

  test(`the helper textarea has focus once the session opens at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await openSession(page)
    expect(await page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"))).toBe(true)
    await expect(page.locator('[data-slot="status-bar"]')).toContainText("claude · default · 100 × 30 · Shared input")
  })

  test(`wheel scrolls back through history at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await openSession(page, { output: Buffer.from(Array.from({ length: 100 }, (_, index) => `history ${index}\r\n`).join("")) })
    const bottom = await page.evaluate(() => window.__agentpackTerminal?.buffer.active.viewportY ?? 0)
    const point = await cell(page, 10, 10)
    await page.mouse.move(point.x, point.y)
    await page.mouse.wheel(0, -600)
    await expect.poll(() => page.evaluate(() => window.__agentpackTerminal?.buffer.active.viewportY ?? 0)).toBeLessThan(bottom)
  })
}
