import { expect, test } from "@playwright/test"
import { cell, dragCells, history, openRoute, screen, serve, session, shared, stubApi, stubWorker } from "./terminal-stub"

const viewports = [{ width: 1100, height: 560 }]

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

  test(`the platform selection modifier drag selects while the app owns the mouse at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const worker = await stubWorker(page)
    worker.behave((conn) => serve(conn, { body: screen("A", { mouseModes: true }) }))
    await openRoute(page, "/sessions/local/s1")
    const modifier = await page.evaluate(() => /Mac/.test(navigator.platform) ? "Alt" : "Shift")
    await page.keyboard.down(modifier)
    await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
    await page.keyboard.up(modifier)
    expect(await page.evaluate(() => window.__agentpackTerminal?.getSelection().length)).toBe(40)
    expect(worker.conns[0]?.sent.some((message) => message.startsWith("\x1b[<"))).toBe(false)
  })

  test(`the helper textarea has focus once the session opens at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await stubWorker(page)
    await openRoute(page, "/sessions/local/s1")
    expect(await page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"))).toBe(true)
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

test("an IME event after typing sends only its own text, never the whole line again", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await page.keyboard.type("hello")
  await expect.poll(() => worker.conns[0]?.sent.join("")).toContain("hello")
  // keyCode 229 is how browsers announce IME, dictation and autocorrect edits.
  await page.evaluate(async () => {
    const input = document.querySelector<HTMLTextAreaElement>(".xterm-helper-textarea")!
    input.dispatchEvent(new KeyboardEvent("keydown", { keyCode: 229, bubbles: true } as KeyboardEventInit))
    input.value = input.value.slice(0, -1) + "X"
    await new Promise((resolve) => setTimeout(resolve))
  })
  await expect.poll(() => worker.conns[0]?.sent.join("")).toContain("X")
  expect(worker.conns[0]?.sent.join("")).toBe("helloX")
})

test("the phone terminal fits the same host grid and opens the keyboard explicitly", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  const input = page.locator(".xterm-helper-textarea")
  await expect(input).not.toBeFocused()
  await expect(page.getByText("Connected · 100×30 host grid")).toBeVisible()
  const grid = () => page.evaluate(() => [window.__agentpackTerminal?.cols, window.__agentpackTerminal?.rows])
  const before = await grid()
  await page.getByRole("button", { name: "Larger" }).click()
  await expect(page.getByRole("button", { name: "Fit" })).toHaveAttribute("aria-pressed", "false")
  await page.getByRole("button", { name: "Smaller" }).click()
  await page.getByRole("button", { name: "Fit" }).click()
  expect(await grid()).toEqual(before)
  expect(worker.conns[0]?.sent).toEqual([])
  await page.getByRole("button", { name: "Keyboard", exact: true }).click()
  await expect(input).toBeFocused()
})

test("Continue last session reopens the terminal Sessions left, with its grouping", async ({ page }) => {
  await stubWorker(page)
  await openRoute(page, "/sessions/local/s1?group=machine")
  await page.getByRole("navigation", { name: "Views" }).getByRole("link", { name: "Usage", exact: true }).click()
  await expect(page).toHaveURL(/\/usage/)
  // The URL changes before the Usage route commits; until the terminal has left, there is nothing to reopen.
  await expect(page.locator(".xterm")).toHaveCount(0)
  await page.getByRole("navigation", { name: "Views" }).getByRole("link", { name: "Sessions", exact: true }).click()
  await expect(page).toHaveURL(/\/sessions$/)
  await page.getByRole("link", { name: "Continue “build”", exact: true }).click()
  await expect(page).toHaveURL(/\/sessions\/local\/s1\?group=machine/)
  await expect(page.locator(".xterm-helper-textarea")).toBeFocused()
})

test("a non-interactive session shows native stdio copy and never opens a socket", async ({ page }) => {
  await stubApi(page, () => [session("n1", "stdio", { state: { ...shared, interactive: false } })])
  const worker = await stubWorker(page)
  await page.goto("/sessions/local/n1")
  await expect(page.getByText(/Native stdio · browser input unavailable/)).toBeVisible()
  await expect(page.locator(".xterm")).toHaveCount(0)
  expect(worker.conns).toHaveLength(0)
})

test("an unknown session on an offline machine explains the missing metadata without opening a terminal", async ({ page }) => {
  const sessionRequests: string[] = []
  await stubApi(page, () => [], [{ id: "n2", name: "far", online: false }])
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    sessionRequests.push(route.request().url())
    return route.fulfill({ json: [] })
  })
  const worker = await stubWorker(page)
  await page.goto("/sessions/n2/s9")
  await expect(page.getByText("This gateway has no saved row for the session.")).toBeVisible()
  await expect(page.locator(".xterm")).toHaveCount(0)
  expect(sessionRequests.filter((url) => url.includes("node=n2"))).toHaveLength(0)
  expect(worker.conns).toHaveLength(0)
})

test("an offline terminal keeps its title, host and last-seen time", async ({ page }) => {
  await stubApi(page, () => [session("s9", "cedar logs tail", {
    node_id: "n2", reachable: false, last_sync: "2026-11-24T19:30:00Z",
  })], [{ id: "n2", name: "cedar", online: false }])
  const worker = await stubWorker(page)
  await page.goto("/sessions/n2/s9")
  await expect(page.getByRole("heading", { level: 1, name: "cedar logs tail" })).toBeVisible()
  await expect(page.getByText(/cedar · Last seen.+Offline/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Retry" })).toBeVisible()
  await expect(page.getByRole("link", { name: "Return to sessions" })).toBeVisible()
  await expect(page.locator(".xterm")).toHaveCount(0)
  expect(worker.conns).toHaveLength(0)
})

test("the terminal toolbar mouse hint follows app mouse tracking", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  const modifier = await page.evaluate(() => /Mac/.test(navigator.platform) ? "⌥" : "⇧")
  const hint = page.getByText(`Mouse: app · ${modifier} drag to select`)
  await expect(hint).toBeHidden()
  worker.conns[0]?.ws.send(Buffer.from("\x1b[?1000h\x1b[?1006h"))
  await expect(hint).toBeVisible()
  worker.conns[0]?.ws.send(Buffer.from("\x1b[?1000l"))
  await expect(hint).toBeHidden()
})

