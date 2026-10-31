import { expect, test, type Locator, type Page, type Route } from "@playwright/test"
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

// Peers update on their own schedule, so the gateway routinely talks to older ones.
test.describe("a peer on an older version", () => {
  test.skip(({ isMobile }) => isMobile, "desktop")

  async function oldPeer(page: Page) {
    await desktop(page)
    await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "old", name: "main", online: true }] }))
    const unlessPeer = (json: unknown, peerJson: unknown, status = 200) => (route: Route) =>
      new URL(route.request().url()).searchParams.has("node") ? route.fulfill({ status, json: peerJson }) : route.fulfill({ json })
    // It ignores ?include=revisions and has no /api/projects.
    await page.route("**/api/library/agentpacks**", unlessPeer([], [{ id: "bare", name: "bare-pack", revisions: ["r1"], heads: ["r1"] }]))
    await page.route("**/api/projects**", unlessPeer([], "404 page not found", 404))
  }

  test("the Library lists its bare agentpacks instead of crashing", async ({ page }) => {
    await oldPeer(page)
    await page.goto("/library")
    await expect(page.getByRole("link", { name: /bare-pack/ })).toBeVisible()
  })

  test("Machines asks for an update, with no raw 404", async ({ page }) => {
    await oldPeer(page)
    await page.goto("/machines/old")
    await expect(page.getByText("Update Agentpack on main to see its projects.")).toBeVisible()
    await expect(page.getByText("404")).toHaveCount(0)
  })
})

test("a confirm alert keeps its stacked buttons inside it", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  await page.goto("/settings/account")
  await page.getByRole("button", { name: "Stop gateway…" }).click()
  const alert = await page.getByRole("alertdialog").boundingBox()
  const stop = await page.getByRole("alertdialog").getByRole("button", { name: "Stop gateway" }).boundingBox()
  const cancel = await page.getByRole("alertdialog").getByRole("button", { name: "Cancel" }).boundingBox()
  expect(stop!.x).toBeGreaterThanOrEqual(alert!.x)
  expect(stop!.x + stop!.width).toBeLessThanOrEqual(alert!.x + alert!.width)
  expect(cancel!.y).toBeGreaterThan(stop!.y)
})

test("a machine with no projects says so in New session", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [] }))
  await page.goto("/sessions?new=1")
  await expect(page.getByText("No projects yet")).toBeVisible()
  await expect(page.getByRole("combobox", { name: "Project" })).toHaveCount(0)
  await expect(page.getByRole("button", { name: "Start session" })).toBeDisabled()
})

test("a grid column can be dragged wider", async ({ page, isMobile, browserName }) => {
  test.skip(isMobile || browserName !== "chromium", "desktop pointer")
  await desktop(page)
  await page.goto("/limits")
  const header = page.getByRole("columnheader", { name: "Window" })
  const before = (await header.boundingBox())!
  const handle = (await page.getByRole("separator", { name: "Resize Window" }).boundingBox())!
  await page.mouse.move(handle.x + 1, handle.y + 5)
  await page.mouse.down()
  await page.mouse.move(handle.x + 81, handle.y + 5, { steps: 3 })
  await page.mouse.up()
  expect((await header.boundingBox())!.width).toBeCloseTo(before.width + 80, -1)
})

test("Update on an Installed in row installs the shown commit there", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  const pin = (commit: string) => ({ module: "github.com/acme/tools", commit: commit.repeat(40), content_hash: "", cache_key: commit, kind: "skill", name: "tools", url: "", direct: true })
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [projectRow("/work/a", [{ harness: "claude" }], { packages: [pin("a")] }), projectRow("/work/b", [{ harness: "claude" }], { packages: [pin("b")] })] }))
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: ["a", "b"].map((commit) => ({ ...pin(commit), id: commit, owner: "acme", repo: "tools" })) }))
  const posted: { spec: string; directory: string }[] = []
  await page.route("**/api/packages", (route) => {
    posted.push(route.request().postDataJSON())
    return route.fulfill({ json: { installed: true } })
  })
  await page.goto("/library/package/github.com/acme/tools")
  await page.getByRole("button", { name: "Update", exact: true }).click()
  await expect.poll(() => posted.length).toBe(1)
  // Exactly one row is behind the shown commit: it moves to the other row's pin, in its own folder.
  const target = posted[0]!
  expect(target.spec).toBe(`github.com/acme/tools@${(target.directory === "/work/a" ? "b" : "a").repeat(40)}`)
})

test("a long project path stays inside the New session sheet", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  const long = `/work/${"very-long-folder-name/".repeat(12)}project`
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [projectRow(long, [{ harness: "claude", mode: "" }])] }))
  await page.goto("/sessions?new=1")
  const sheet = (await page.getByRole("dialog").boundingBox())!
  const start = (await page.getByRole("button", { name: "Start session" }).boundingBox())!
  const project = (await page.getByRole("combobox", { name: "Project" }).boundingBox())!
  expect(start.x + start.width).toBeLessThanOrEqual(sheet.x + sheet.width)
  expect(project.x + project.width).toBeLessThanOrEqual(sheet.x + sheet.width)
  // A CLI launch records an empty mode; the field still names the real one.
  await expect(page.getByLabel("Mode")).toHaveValue("default")
})

test("a stop in progress shows on its row until it finishes", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  let finish = () => {}
  const held = new Promise<void>((resolve) => (finish = resolve))
  await page.route("**/api/sessions/local-one/stop", async (route) => {
    await held
    return route.fulfill({ json: { stopped: true } })
  })
  await page.goto("/sessions")
  const wrapper = page.locator('[data-row-key="local:local-one"]').locator("..")
  await wrapper.hover()
  await wrapper.getByRole("button", { name: "More actions" }).click()
  await page.getByRole("menuitem", { name: "Stop…" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Stop agent" }).click()
  await expect(wrapper).toContainText("Stopping…")
  finish()
  await expect(wrapper).not.toContainText("Stopping…")
})
