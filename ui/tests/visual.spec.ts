import { expect, test, type Locator, type Page } from "@playwright/test"
import { mockAPI, projectRow, session } from "./helpers"

// The kit's look is enforced by CSS, which unit tests and role queries cannot see. A cascade mistake
// once made every link blue, every selected row invisible and every font size inert, with all tests green.
const style = (locator: Locator, property: string, pseudo?: string) =>
  locator.evaluate((element, [name, pseudoElement]) => getComputedStyle(element, pseudoElement).getPropertyValue(name!), [property, pseudo] as const)

const transparent = "rgba(0, 0, 0, 0)"

async function desktop(page: Page) {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockAPI(page)
}

test.describe("kit styling", () => {
  test.skip(({ isMobile }) => isMobile, "desktop chrome")

  test("base rules do not outrank utilities", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions")
    const row = page.locator('[data-row-key="local:local-one"]')
    await expect(row).toBeVisible()
    // Link text takes the foreground, not an accent link colour.
    expect(await style(row, "color")).toBe(await page.locator("body").evaluate((body) => getComputedStyle(body).color))
    // A real font-size utility: the toolbar subtitle is the 11 px token.
    await expect(page.getByText("2 sessions on 1 machine")).toBeVisible()
    expect(await style(page.getByText("2 sessions on 1 machine"), "font-size")).toBe("11px")
  })

  test("the selected row is painted and keeps contrast off focus", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions/local/local-one")
    const row = page.locator('[data-row-key="local:local-one"]')
    await expect(row).toHaveAttribute("data-selected", "true")
    const unfocused = await style(row, "background-color")
    expect(unfocused).not.toBe(transparent)
    // Gray selection keeps dark text; white belongs to the accent fill.
    expect(await style(row, "color")).toBe(await style(page.locator('[data-row-key="local:local-two"]'), "color"))
    await row.focus()
    await expect.poll(() => style(row, "background-color")).not.toBe(unfocused)
  })

  test("segmented controls show which segment is on", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions")
    const on = page.getByRole("button", { name: "Machine", exact: true })
    await expect(on).toHaveAttribute("aria-pressed", "true")
    expect(await style(on, "background-color")).not.toBe(await style(page.getByRole("button", { name: "Project", exact: true }), "background-color"))
  })

  test("popup selects show the label, not the raw value", async ({ page }) => {
    await desktop(page)
    await page.goto("/library")
    await expect(page.getByRole("combobox", { name: "Sort" })).toContainText("Type")
    expect((await page.getByRole("combobox", { name: "Sort" }).boundingBox())?.height).toBe(24)
  })

  test("meter colours follow what is left", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/limits", (route) =>
      route.fulfill({ json: { limits: [{ harness: "codex", state: "available", observed_at: new Date().toISOString(), windows: [
        { id: "a", label: "Plenty", remaining_percent: 70 },
        { id: "b", label: "Low", remaining_percent: 10 },
        { id: "c", label: "Out", remaining_percent: 2 },
      ] }] } }),
    )
    await page.goto("/limits")
    const fill = (name: string) => page.getByRole("meter", { name: `${name} remaining` }).evaluate((meter) => getComputedStyle(meter).getPropertyValue("--meter"))
    await expect(page.getByRole("meter", { name: "Low remaining" })).toBeVisible()
    expect(new Set([await fill("Plenty"), await fill("Low"), await fill("Out")]).size).toBe(3)
  })

  test("the topology diagram is drawn at its own size", async ({ page, browserName }) => {
    test.skip(browserName !== "chromium", "layout only")
    await desktop(page)
    await page.route("**/api/topology", (route) =>
      route.fulfill({ json: { self: { id: "g", name: "gw", os: "linux", relay: "" }, peers: [{ id: "p", name: "peer", os: "linux", state: "reachable", connection: "direct", relay: "", sessions: 0 }] } }),
    )
    await page.goto("/machines/topology")
    const box = await page.locator('[data-slot="topology-diagram"]').boundingBox()
    expect(box?.width).toBeGreaterThan(300)
  })

  test("the terminal background fills its pane in the active theme", async ({ page }) => {
    await desktop(page)
    await page.emulateMedia({ colorScheme: "light" })
    await page.goto("/sessions")
    await expect(page.getByRole("link", { name: "Settings" })).toBeVisible()
    expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--terminal-bg").trim())).toMatch(/^#f{3,6}$/)
  })
})

test.describe("session list behaviour", () => {
  test.skip(({ isMobile }) => isMobile, "desktop keyboard")

  test("?q filters rows and survives reload", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions")
    await page.getByRole("searchbox", { name: "Filter sessions" }).fill("another")
    await expect(page).toHaveURL(/q=another/)
    await expect(page.locator('[data-row-key="local:local-one"]')).toHaveCount(0)
    await page.reload()
    await expect(page.locator('[data-row-key="local:local-two"]')).toBeVisible()
    await expect(page.locator('[data-row-key="local:local-one"]')).toHaveCount(0)
  })

  test("opening a session keeps the grouping", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions?group=project")
    await page.locator('[data-row-key="local:local-one"]').click()
    await expect(page).toHaveURL(/\/sessions\/local\/local-one\?group=project/)
  })

  test("arrows stay in the list while rows open; Enter moves to the terminal", async ({ page }) => {
    await desktop(page)
    await page.routeWebSocket("**/api/sessions/*/terminal*", (ws) => {
      ws.send(JSON.stringify({ type: "attached", attachment: 1, offset: 0, state: session.state }))
      ws.send(JSON.stringify({ type: "replay_complete" }))
    })
    await page.goto("/sessions")
    await page.locator('[data-row-key="local:local-two"]').focus()
    await page.keyboard.press("ArrowDown")
    await expect(page).toHaveURL(/\/sessions\/local\/local-one$/)
    await page.locator(".xterm-helper-textarea").waitFor({ state: "attached" })
    // Opening a row on the way must not steal focus, or the next arrow would be typed into the agent.
    await expect(page.locator('[data-row-key="local:local-one"]')).toBeFocused()
    await page.keyboard.press("Enter")
    await expect(page.locator(".xterm-helper-textarea")).toBeFocused()
  })

  test("clicking a row still moves focus to the terminal", async ({ page }) => {
    await desktop(page)
    await page.routeWebSocket("**/api/sessions/*/terminal*", (ws) => {
      ws.send(JSON.stringify({ type: "attached", attachment: 1, offset: 0, state: session.state }))
      ws.send(JSON.stringify({ type: "replay_complete" }))
    })
    await page.goto("/sessions")
    await page.locator('[data-row-key="local:local-one"]').click()
    await expect(page.locator(".xterm-helper-textarea")).toBeFocused()
  })

  test("Install package lists known packages with their state", async ({ page }) => {
    await desktop(page)
    const pin = { module: "github.com/acme/tools", commit: "a".repeat(40), content_hash: "", cache_key: "k", kind: "skill", name: "tools", url: "", direct: true }
    await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow("/work/my-project", [{ harness: "claude" }], { packages: [pin] }), projectRow("/work/another-project", [{ harness: "claude" }])] }))
    await page.route("**/api/library/packages", (route) => route.fulfill({ json: [{ ...pin, id: "k", owner: "acme", repo: "tools" }] }))
    const posted: { spec?: string; directory?: string }[] = []
    await page.route("**/api/packages", (route) => {
      posted.push(route.request().postDataJSON())
      return route.fulfill({ json: { installed: true } })
    })
    await page.goto("/sessions")
    const row = page.locator('[data-row-key="local:local-two"]')
    await row.hover()
    await page.getByRole("button", { name: "More actions" }).first().click()
    await page.getByRole("menuitem", { name: "Install package…" }).click()
    const known = page.getByRole("group", { name: "Known packages" })
    await expect(known.getByText("tools")).toBeVisible()
    await known.getByRole("checkbox").click()
    await page.getByRole("button", { name: "Install", exact: true }).click()
    await expect.poll(() => posted.length).toBe(1)
    expect(posted[0]).toMatchObject({ spec: `github.com/acme/tools@${"a".repeat(40)}`, directory: "/work/another-project" })
  })
})

test("a failing machine list still reports the gateway connection", async ({ page, isMobile }) => {
  test.skip(isMobile, "the status bar is desktop chrome")
  await desktop(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ status: 400, json: { error: "tailscale is not running" } }))
  await page.goto("/sessions")
  await expect(page.getByRole("contentinfo")).toContainText("Connected · 1 machine")
})
