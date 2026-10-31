import { expect, test, type Page } from "@playwright/test"
import { cell, dragCells, openRoute, screen, serve, session, stubApi, stubWorker } from "./terminal-stub"

test.beforeEach(async ({ page }) => {
  await stubApi(page, () => [session("s1", "build")])
})

// Records what the page would copy. The real clipboard is not readable in every browser under test.
async function stubClipboard(page: Page, readText: string) {
  await page.evaluate((text) => {
    Object.defineProperty(navigator.clipboard, "writeText", { configurable: true, value: async (value: string) => (window.__clip = value) })
    Object.defineProperty(navigator.clipboard, "readText", { configurable: true, value: async () => text })
  }, readText)
}

test("Ctrl+C with a selection copies the selected text and sends no interrupt", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await page.evaluate(() => {
    document.addEventListener("copy", (event) => {
      window.__copied = event.clipboardData?.getData("text/plain") ?? null
    })
  })
  await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
  await expect.poll(() => page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"))).toBe(true)
  await page.keyboard.press("Control+C")
  await expect.poll(() => page.evaluate(() => window.__copied?.length ?? 0)).toBe(40)
  expect(worker.conns[0]?.sent.some((message) => message.includes("\x03"))).toBe(false)
})

test("Ctrl+C without a selection still sends the interrupt", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await expect.poll(() => page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"))).toBe(true)
  await page.keyboard.press("Control+C")
  await expect.poll(() => worker.conns[0]?.sent).toContain("\x03")
})

test("Ctrl+Shift+C copies the selection through the clipboard API", async ({ page }) => {
  await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await stubClipboard(page, "")
  await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
  await expect.poll(() => page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"))).toBe(true)
  await page.keyboard.press("Control+Shift+C")
  await expect.poll(() => page.evaluate(() => window.__clip?.length ?? 0)).toBe(40)
})

test("Ctrl+Shift+V pastes clipboard text into the session", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await stubClipboard(page, "pasted")
  await expect.poll(() => page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"))).toBe(true)
  await page.keyboard.press("Control+Shift+V")
  await expect.poll(() => worker.conns[0]?.sent).toContain("pasted")
})

test("the right-click menu copies the selection", async ({ page }) => {
  await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await stubClipboard(page, "")
  await dragCells(page, { x: 20, y: 20 }, { x: 60, y: 20 })
  const point = await cell(page, 40, 20)
  await page.mouse.click(point.x, point.y, { button: "right" })
  await page.getByRole("menuitem", { name: "Copy", exact: true }).click()
  await expect.poll(() => page.evaluate(() => window.__clip?.length ?? 0)).toBe(40)
})

test("the right-click menu pastes clipboard text into the session", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await stubClipboard(page, "menu")
  const point = await cell(page, 40, 20)
  await page.mouse.click(point.x, point.y, { button: "right" })
  await page.getByRole("menuitem", { name: "Paste", exact: true }).click()
  await expect.poll(() => worker.conns[0]?.sent).toContain("menu")
})
