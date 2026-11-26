import { expect, test } from "@playwright/test"
import { mockAPI, session } from "./helpers"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }

test.beforeEach(async ({ page }) => {
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) =>
    route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }),
  )
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
})

test("view digits follow the physical key, whatever the layout types", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop keyboard")
  await mockAPI(page)
  await page.goto("/sessions")
  const views = page.getByRole("navigation", { name: "Views" })
  await expect(views).toBeVisible()
  // AZERTY: the unshifted digit row types punctuation.
  await page.evaluate(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "&", code: "Digit2", metaKey: true, bubbles: true })))
  await expect(page).toHaveURL(/\/library$/)
  await page.keyboard.press("Meta+3")
  await expect(page).toHaveURL(/\/usage$/)
  await expect(views.getByRole("link", { name: "System" })).toHaveAttribute("aria-current", "page")
})

test("sidebar toggle is on every list-detail view and survives view switch and reload", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const toggle = page.getByRole("button", { name: "Toggle sidebar" })
  const sidebar = page.getByRole("region", { name: "Sidebar" })

  for (const path of ["/sessions", "/library", "/usage", "/settings/general"]) {
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

test("390 px shows all three tabs, none covered", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  // /limits has the widest content: its table must not widen the window and push tabs off screen.
  for (const path of ["/sessions", "/limits"]) {
    await page.goto(path)
    const tabs = page.getByRole("navigation", { name: "Tabs" }).getByRole("link")
    await expect(tabs).toHaveCount(3)

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

test("phone Back remembers the filtered Library across search changes, browser history, and reload", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/library?q=alpha")
  await page.getByRole("link", { name: "Discover" }).click()
  await page.getByRole("searchbox", { name: "Search catalog" }).fill("beta")
  const back = page.getByRole("link", { name: "Back to Library" })
  await expect(back).toHaveAttribute("href", "/library?q=alpha")
  await page.goBack()
  await expect(page).toHaveURL(/\/library\?q=alpha$/)
  await page.goForward()
  await page.reload()
  await back.click()
  await expect(page).toHaveURL(/\/library\?q=alpha$/)
  await expect(page.getByRole("searchbox", { name: "Search library" })).toHaveValue("alpha")
})

test("the phone System tab opens Usage; Back and a second tap open its list", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/sessions")
  const system = page.getByRole("navigation", { name: "Tabs" }).getByRole("link", { name: "System" })
  const list = page.getByRole("navigation", { name: "System" })
  await system.click()
  await expect(page).toHaveURL(/\/usage$/)
  await system.click()
  await expect(page).toHaveURL(/\/system$/)
  await list.getByRole("link", { name: /^Limits/ }).click()
  await expect(page).toHaveURL(/\/usage\/limits$/)
  await page.getByRole("link", { name: "Back to System" }).click()
  await expect(page).toHaveURL(/\/system$/)
  await expect(list).toBeVisible()
})

test("a direct phone Settings pane returns to the System list", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/settings/general")
  await page.getByRole("link", { name: "Back to System" }).click()
  await expect(page).toHaveURL(/\/system$/)
  await expect(page.getByRole("link", { name: "Notifications" })).toBeVisible()
})

test("phone System stays selected at its list, topology and a machine", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 390, height: 844 })
  for (const path of ["/system", "/machines/topology", "/machines/local"]) {
    await page.goto(path)
    await expect(page.getByRole("navigation", { name: "Tabs" }).getByRole("link", { name: "System" })).toHaveAttribute("aria-current", "page")
  }
})

test("phone session detail frees the tab space and Back preserves list filters", async ({ page }) => {
  await mockAPI(page)
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [{ ...session, title: "Alpha", transport: "acp" }] }))
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/sessions?tab=all&q=Alpha&group=machine")
  await page.locator('[data-row-key="local:local-one"]').click()
  await expect(page.getByRole("navigation", { name: "Tabs" })).toBeHidden()
  await page.getByRole("link", { name: "Back to Sessions" }).click()
  await expect(page).toHaveURL(/\/sessions\?tab=all&q=Alpha&group=machine$/)
})

test("the skip link and palette are visible entry points and Settings moves focus to its detail heading", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop keyboard")
  await mockAPI(page)
  await page.goto("/settings/general")
  const skip = page.getByRole("link", { name: "Skip to content" })
  await expect(skip).toBeAttached()
  await page.keyboard.press("Tab")
  await expect(skip).toBeFocused()
  await page.keyboard.press("Enter")
  await expect(page.getByRole("main")).toBeFocused()
  await page.getByRole("link", { name: "Appearance", exact: true }).click()
  const heading = page.getByRole("heading", { level: 1 })
  await expect(heading).toHaveCount(1)
  await expect(heading).toBeFocused()
  await page.getByRole("button", { name: "Command palette" }).click()
  await expect(page.getByRole("dialog", { name: "Command palette" })).toBeVisible()
})

test("the Command palette stays in reach in the 320 px Library toolbar", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto("/library")
  const toolbar = page.locator('[data-slot="toolbar"]')
  const commands = toolbar.getByRole("button", { name: "Command palette" })
  await expect(commands).toBeVisible()
  await expect(toolbar.getByRole("button", { name: "Save agentpack…" })).toBeVisible()
  const box = (await commands.boundingBox())!
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(320)
  expect(box.width).toBeGreaterThanOrEqual(44)
  expect(box.height).toBeGreaterThanOrEqual(44)
  await commands.click()
  const palette = page.getByRole("dialog", { name: "Command palette" })
  await expect(palette).toBeVisible()
  const bounds = (await palette.boundingBox())!
  expect(bounds.x).toBeGreaterThanOrEqual(0)
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(320)
})

test("the palette names the branch and requires confirmation before pushing it", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop command palette")
  await mockAPI(page)
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [{
    ...session, transport: "acp", directory: "/ap/worktrees/0123456789abcdef/review",
  }] }))
  await page.route("**/api/sessions/local-one/git", (route) => route.fulfill({ json: {
    available: true, worktree: true, branch: "fix/login", ahead: 1,
  } }))
  let pushes = 0
  await page.route("**/api/sessions/local-one/git/pr", (route) => {
    pushes++
    return route.fulfill({ json: { created: true, url: "https://github.com/example/review/pull/12" } })
  })
  await page.goto("/sessions/local/local-one")
  await page.getByRole("button", { name: "Command palette" }).click()
  const search = page.getByRole("combobox", { name: "Search sessions and commands" })
  await search.fill("Create pull request")
  await page.getByRole("option", { name: "Create pull request…" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("Pushes branch fix/login to origin")
  await expect(confirm.getByRole("button", { name: "Cancel" })).toBeFocused()
  expect(pushes).toBe(0)
  await confirm.getByRole("button", { name: "Create pull request" }).click()
  await expect.poll(() => pushes).toBe(1)
  await page.reload()
  await expect(page.getByRole("banner").getByRole("link", { name: "Open pull request" })).toHaveAttribute("href", "https://github.com/example/review/pull/12")
})
