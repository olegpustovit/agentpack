import { expect, test } from "@playwright/test"
import { cell, dragCells, history, openRoute, screen, serve, session, shared, statusBar, stubApi, stubWorker } from "./terminal-stub"

const viewports = [
  { width: 1440, height: 900 },
  { width: 1100, height: 560 },
  { width: 800, height: 400 },
]

test.beforeEach(async ({ page }) => {
  await stubApi(page, () => [session("s1", "build")])
})

for (const viewport of viewports) {
  const size = `${viewport.width}×${viewport.height}`

  test(`drag from cell (20,20) to (60,20) selects 40 cells at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await stubWorker(page)
    await openRoute(page, "/sessions/local/s1")
    await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
    expect(await page.evaluate(() => window.__agentpackTerminal?.getSelectionPosition())).toEqual({ start: { x: 20, y: 20 }, end: { x: 60, y: 20 } })
    expect(await page.evaluate(() => window.__agentpackTerminal?.getSelection().length)).toBe(40)
  })

  test(`a click with app mouse tracking sends the SGR press for cell (60,20) at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const worker = await stubWorker(page)
    worker.behave((conn) => serve(conn, { body: screen("A", { mouseModes: true }) }))
    await openRoute(page, "/sessions/local/s1")
    const point = await cell(page, 60, 20)
    await page.mouse.click(point.x, point.y)
    await expect.poll(() => worker.conns[0]?.sent).toContain("\x1b[<0;61;21M")
  })

  test(`shift-drag selects while the app owns the mouse at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const worker = await stubWorker(page)
    worker.behave((conn) => serve(conn, { body: screen("A", { mouseModes: true }) }))
    await openRoute(page, "/sessions/local/s1")
    await page.keyboard.down("Shift")
    await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
    await page.keyboard.up("Shift")
    expect(await page.evaluate(() => window.__agentpackTerminal?.getSelection().length)).toBe(40)
    expect(worker.conns[0]?.sent.some((message) => message.startsWith("\x1b[<"))).toBe(false)
  })

  test(`the helper textarea has focus once the session opens at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await stubWorker(page)
    await openRoute(page, "/sessions/local/s1")
    expect(await page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"))).toBe(true)
    await expect(statusBar(page)).toContainText("Claude · default · 100 × 30 · Shared input")
  })

  test(`wheel scrolls back through history at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const worker = await stubWorker(page)
    worker.behave((conn) => serve(conn, { body: history(100) }))
    await openRoute(page, "/sessions/local/s1")
    const bottom = await page.evaluate(() => window.__agentpackTerminal?.buffer.active.viewportY ?? 0)
    const point = await cell(page, 10, 10)
    await page.mouse.move(point.x, point.y)
    await page.mouse.wheel(0, -600)
    await expect.poll(() => page.evaluate(() => window.__agentpackTerminal?.buffer.active.viewportY ?? 0)).toBeLessThan(bottom)
  })
}

test("an agy desktop session shows its banner and never opens a socket", async ({ page }) => {
  await stubApi(page, () => [session("a1", "desk", { harness: "agy" })])
  const worker = await stubWorker(page)
  await page.goto("/sessions/local/a1")
  await expect(page.getByRole("status").filter({ hasText: "Desktop launcher presence" })).toBeVisible()
  await expect(statusBar(page)).not.toContainText(" × ")
  expect(worker.conns).toHaveLength(0)
})

test("a non-interactive session shows native stdio copy and never opens a socket", async ({ page }) => {
  await stubApi(page, () => [session("n1", "stdio", { state: { ...shared, interactive: false } })])
  const worker = await stubWorker(page)
  await page.goto("/sessions/local/n1")
  await expect(page.getByRole("status").filter({ hasText: "Native stdio · browser input unavailable" })).toBeVisible()
  await expect(statusBar(page)).not.toContainText(" × ")
  expect(worker.conns).toHaveLength(0)
})

test("a session on an offline machine shows the last-known banner and fetches nothing", async ({ page }) => {
  const sessionRequests: string[] = []
  await stubApi(page, () => [], [{ id: "n2", name: "far", online: false }])
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    sessionRequests.push(route.request().url())
    return route.fulfill({ json: [] })
  })
  const worker = await stubWorker(page)
  await page.goto("/sessions/n2/s9")
  await expect(page.getByRole("status").filter({ hasText: "Offline · showing last-known session metadata" })).toBeVisible()
  await expect(statusBar(page)).not.toContainText(" × ")
  expect(sessionRequests.filter((url) => url.includes("node=n2"))).toHaveLength(0)
  expect(worker.conns).toHaveLength(0)
})

test("the status slot shows the session line and the mouse hint follows app mouse tracking", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await expect(statusBar(page)).toContainText("Claude · default · 100 × 30 · Shared input")
  await expect(statusBar(page)).not.toContainText("Mouse: app")
  worker.conns[0]?.ws.send(Buffer.from("\x1b[?1000h\x1b[?1006h"))
  await expect(statusBar(page)).toContainText("Mouse: app · ⇧ drag to select")
  worker.conns[0]?.ws.send(Buffer.from("\x1b[?1000l"))
  await expect(statusBar(page)).not.toContainText("Mouse: app")
})

for (const path of ["/limits", "/settings"]) {
  test(`the session slot is absent on ${path}`, async ({ page }) => {
    await stubWorker(page)
    await openRoute(page, "/sessions/local/s1")
    await page.getByRole("link", { name: path === "/limits" ? "Limits" : "Settings" }).first().click()
    await expect(page).toHaveURL(new RegExp(path))
    await expect(statusBar(page)).not.toContainText(" × ")
    await expect(statusBar(page)).not.toContainText("Shared input")
  })
}
