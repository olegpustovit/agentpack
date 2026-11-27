import { expect, test } from "@playwright/test"
import { mockAPI } from "./helpers"

const NOW = new Date("2026-11-18T15:00:00Z")

test("a phone fits one row per agent with its tightest limit and reset", async ({ page }) => {
  await page.clock.setFixedTime(NOW)
  await page.setViewportSize({ width: 390, height: 844 })
  await mockAPI(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "peer-1", name: "Mac Studio", online: true }] }))
  await page.route("**/api/limits", (route) => route.fulfill({ json: { limits: [
    { harness: "codex", state: "available", observed_at: "2026-11-18T14:30:00Z", windows: [
      { id: "session", label: "Session", remaining_percent: 2, reset_at: "2026-11-18T16:00:00Z" },
    ] },
    { node_id: "peer-1", harness: "codex", state: "available", observed_at: "2026-11-18T14:59:00Z", windows: [
      { id: "session", label: "Session", remaining_percent: 75, reset_at: "2026-11-18T16:00:00Z" },
      { id: "weekly", label: "Weekly", remaining_percent: 36, reset_at: "2026-11-20T15:00:00Z" },
    ] },
    { harness: "claude", state: "available", observed_at: "2026-11-18T14:58:00Z", windows: [
      { id: "session", label: "Session", remaining_percent: 4, reset_at: "2026-11-18T17:00:00Z", duration_seconds: 18_000 },
    ] },
    { harness: "grok", state: "error", error: "authentication required", windows: [] },
    { node_id: "peer-1", harness: "grok", state: "error", error: "authentication required", windows: [] },
    { harness: "omp", state: "available", windows: [{ id: "weekly", label: "Weekly", remaining_percent: 5 }] },
    { harness: "cursor", state: "unsupported", error: "cursor CLI not found", windows: [] },
    { node_id: "peer-1", harness: "cursor", state: "unsupported", error: "cursor CLI not found", windows: [] },
  ] } }))

  await page.goto("/usage/limits")

  const list = page.getByRole("list", { name: "Limits", exact: true })
  const rows = list.getByRole("listitem")
  await expect(rows).toHaveCount(3)
  const codex = rows.filter({ has: page.getByText("Codex", { exact: true }) })
  await expect(codex.getByRole("meter", { name: "Codex left" })).toHaveAttribute("value", "36")
  await expect(codex.getByText("36%", { exact: true })).toBeVisible()
  await expect(codex.getByText("resets in 2 d 0 h", { exact: true })).toBeVisible()

  const claude = rows.filter({ has: page.getByText("Claude", { exact: true }) })
  const pace = claude.getByText("Runs out in about 8 m at this pace · resets in 2 h 0 m")
  await expect(pace).toBeVisible()
  expect(await pace.evaluate((element) => {
    const color = getComputedStyle(element).color
    const swatch = document.createElement("span")
    swatch.style.color = "var(--warn-word)"
    element.append(swatch)
    const warn = getComputedStyle(swatch).color
    swatch.remove()
    return color === warn
  })).toBe(true)

  const grok = rows.filter({ has: page.getByText("Grok", { exact: true }) })
  await expect(grok.getByRole("link", { name: "Sign in", exact: true })).toHaveAttribute("href", "/settings/connections")
  await expect(page.locator('[data-slot="toolbar"]')).toContainText("Updated 1 minute ago")
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test("a stale reading shows its age", async ({ page }) => {
  await page.clock.setFixedTime(NOW)
  await mockAPI(page)
  await page.route("**/api/limits", (route) => route.fulfill({ json: { limits: [
    { harness: "codex", state: "available", observed_at: "2026-11-18T06:00:00Z", windows: [
      { id: "session", label: "Session", remaining_percent: 72, reset_at: "2026-11-18T07:00:00Z" },
    ] },
  ] } }))

  await page.goto("/usage/limits")
  await expect(page.getByRole("list", { name: "Limits", exact: true }).getByText("As of 9 hours ago")).toBeVisible()
})

test("the Limits shortcut opens the page and nudges each online peer at most every five minutes", async ({ page }) => {
  await page.clock.install({ time: NOW })
  const requests = await mockAPI(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [
    { id: "peer-1", name: "Mac Studio", online: true },
    { id: "peer-2", name: "Build server", online: true },
    { id: "offline", name: "Offline Mac", online: false },
  ] }))
  const nudged = () => requests.filter(({ method, path }) => method === "POST" && path === "/api/limits/refresh")
    .map(({ query }) => query.get("node")).sort()
  const sidebar = page.getByRole("navigation", { name: "System" })
  const meter = page.getByRole("meter", { name: "Codex left" })

  await page.goto("/limits")
  await expect(page).toHaveURL(/\/usage\/limits$/)
  await expect.poll(nudged).toEqual(["peer-1", "peer-2"])

  await sidebar.getByRole("link", { name: "Usage", exact: true }).click()
  await page.clock.fastForward(30_000)
  await sidebar.getByRole("link", { name: "Limits", exact: true }).click()
  await expect(meter).toBeVisible()
  expect(nudged()).toEqual(["peer-1", "peer-2"])

  await page.clock.fastForward(3 * 60_000)
  await expect(meter).toBeVisible()
  expect(nudged()).toEqual(["peer-1", "peer-2"])

  await page.clock.fastForward(2 * 60_000)
  await expect.poll(nudged).toEqual(["peer-1", "peer-1", "peer-2", "peer-2"])
})
