import { expect, test } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }

test.beforeEach(async ({ page }) => {
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) =>
    route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }),
  )
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
})

test("sidebar toggle is on every list-detail view and survives view switch and reload", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const toggle = page.getByRole("button", { name: "Toggle sidebar" })
  const sidebar = page.getByRole("region", { name: "Sidebar" })

  for (const path of ["/sessions", "/library", "/machines", "/settings/general"]) {
    await page.goto(path)
    await expect(toggle).toBeVisible()
    await expect(toggle).toHaveAttribute("aria-expanded", "true")
    await expect(sidebar).toBeVisible()
  }

  await page.keyboard.press("Meta+Backslash")
  await expect(toggle).toHaveAttribute("aria-expanded", "false")
  await expect(sidebar).toBeHidden()

  await page.keyboard.press("Meta+2")
  await expect(page).toHaveURL(/\/library$/)
  await expect(toggle).toHaveAttribute("aria-expanded", "false")

  await page.reload()
  await expect(toggle).toHaveAttribute("aria-expanded", "false")
  await expect(sidebar).toBeHidden()
})

test("the tab title counts the sessions that need you", async ({ page }) => {
  const state = { interactive: true, running: true, restarting: false, rows: 30, columns: 100, exit_code: 0 }
  const chat = { id: "s1", directory: "/w/alpha", harness: "claude", mode: "default", transport: "acp", conversation_state: "needs_you", state }
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: [chat, { ...chat, id: "s2", conversation_state: "idle" }] }))
  await page.goto("/settings/general")
  await expect(page).toHaveTitle("(1) Agentpack")
})

test("390 px shows all five tabs, none covered", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  // /limits has the widest content: its table must not widen the window and push tabs off screen.
  for (const path of ["/sessions", "/limits"]) {
    await page.goto(path)
    const tabs = page.getByRole("navigation", { name: "Tabs" }).getByRole("link")
    await expect(tabs).toHaveCount(5)

    for (const tab of await tabs.all()) {
      const href = await tab.getAttribute("href")
      const box = await tab.boundingBox()
      if (!box) throw new Error("tab has no bounding box")
      const hit = await page.evaluate(
        ({ x, y }) => document.elementFromPoint(x, y)?.closest("a")?.getAttribute("href") ?? null,
        { x: box.x + box.width / 2, y: box.y + box.height / 2 },
      )
      expect(hit).toBe(href)
    }
  }
})
