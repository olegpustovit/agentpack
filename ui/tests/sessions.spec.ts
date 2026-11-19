import { expect, test } from "@playwright/test"
import { mockAPI } from "./helpers"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
const terminal = (running: boolean) => ({ interactive: true, running, restarting: false, rows: 30, columns: 100, exit_code: 0 })

const sessions = [
  {
    id: "s1",
    name: "build",
    directory: "/home/oleg/agentpack",
    harness: "claude",
    mode: "default",
    state: terminal(true),
    started_at: twoHoursAgo,
    last_used: twoHoursAgo,
    started_by: { kind: "browser", user: "oleg@" },
  },
  {
    id: "s2",
    directory: "/home/oleg/oracle",
    harness: "codex",
    mode: "plan",
    state: terminal(false),
    started_at: twoHoursAgo,
    last_used: twoHoursAgo,
  },
]
const renamed = [sessions[0], { ...sessions[1], name: "oracle main" }]

// What GET /api/sessions returns. Tests change it to simulate new data on the next poll.
let reply: unknown[] = sessions
let fetches = 0

test.beforeEach(async ({ page }) => {
  await mockAPI(page)
  reply = sessions
  fetches = 0
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
  // Aborting the stream makes the list poll on its 15 s fallback, as it does without live events.
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    fetches += 1
    return route.fulfill({ json: reply })
  })
})

test("focus on a row survives a refetch that changes the data", async ({ page }) => {
  await page.clock.install()
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows).toHaveCount(2)
  await rows.first().focus()

  reply = renamed
  const polls = fetches
  await page.clock.runFor(16_000)
  await expect.poll(() => fetches).toBeGreaterThan(polls)
  await expect(page.getByRole("link", { name: /oracle main/ })).toBeVisible()
  await expect(rows.first()).toBeFocused()
})

test("arrow keys open each session at once and leave one history entry", async ({ page }) => {
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await rows.first().focus()

  const before = await page.evaluate(() => history.length)
  await page.keyboard.press("ArrowDown")
  await page.keyboard.press("ArrowUp")
  await page.keyboard.press("ArrowDown")
  await expect(page).toHaveURL(/\/sessions\/local\/s2$/)
  expect(await page.evaluate(() => history.length)).toBe(before + 1)
})

test("New session posts Cursor CLI as the agent harness", async ({ page }) => {
  const project = {
    directory: "/home/oleg/agentpack",
    name: "agentpack",
    folder_id: "f1",
    available: true,
    unavailable_reason: "",
    last_used: twoHoursAgo,
    launches: [],
    packages: [],
    mcp: [],
    error: "",
  }
  await page.route("**/api/projects", (route) => route.fulfill({ json: [project] }))
  // Only the POST is answered here; GET falls through to the shared sessions mock.
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    return route.fulfill({ json: { ...sessions[0], id: "s9" } })
  })

  await page.goto("/sessions?new=1")
  await page.getByRole("group", { name: "Agent" }).getByRole("button", { name: "Cursor CLI" }).click()
  const posted = page.waitForRequest((request) => request.method() === "POST" && /\/api\/sessions$/.test(request.url()))
  await page.getByRole("button", { name: "Start session" }).click()
  const body = (await posted).postDataJSON()
  expect(body).toMatchObject({ directory: "/home/oleg/agentpack", harness: "agent" })
})

test("New session shows what the chosen mode loads", async ({ page }) => {
  const project = {
    directory: "/home/oleg/agentpack",
    name: "agentpack",
    available: true,
    last_used: twoHoursAgo,
    launches: [{ harness: "claude", mode: "default", last_used: twoHoursAgo }],
    packages: [{ module: "github.com/acme/heavy", name: "heavy", kind: "plugin", direct: true }],
    mcp: [{ name: "playwright", launcher: "npm", status: "pinned", package: "@playwright/mcp", version: "1.0.0", source: "plugin" }],
    modes: [
      { name: "default", off_packages: [], partial_packages: [], off_mcp: [] },
      { name: "design", off_packages: ["github.com/acme/heavy"], partial_packages: [], off_mcp: ["playwright"] },
    ],
    error: "",
  }
  await page.route("**/api/projects", (route) => route.fulfill({ json: [project] }))
  await page.goto("/sessions?new=1")
  const loaded = page.getByRole("region", { name: "Loaded in this session" })
  await expect(loaded).toContainText("2 active")
  await expect(loaded).toContainText("@playwright/mcp@1.0.0")
  await page.getByRole("group", { name: "Mode" }).getByRole("button", { name: "design" }).click()
  await expect(loaded).toContainText("0 active, 2 turned off by mode “design”")
  await expect(loaded.getByRole("listitem").filter({ hasText: "heavy" })).toContainText("off in design")
})

test("+ starts a chat at once with the folder's last agent and holds a message until it is up", async ({ page }) => {
  await page.clock.install()
  const starting = { ...sessions[1], id: "s3", transport: "acp", conversation_state: "starting", state: terminal(false) }
  let posted: unknown
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    posted = route.request().postDataJSON()
    reply = [...sessions, starting]
    return route.fulfill({ json: starting })
  })
  const prompts: unknown[] = []
  await page.route("**/api/sessions/s3/conversation/prompt", (route) => {
    prompts.push(route.request().postDataJSON())
    return route.fulfill({ json: {} })
  })
  await page.goto("/sessions")
  // The folder's last agent is Codex in mode "plan"; the sheet is not involved.
  await page.getByRole("button", { name: "New Codex chat in oracle on host-a" }).click()
  await expect(page).toHaveURL(/\/sessions\/[^/]+\/s3/)
  expect(posted).toMatchObject({ directory: "/home/oleg/oracle", harness: "codex", mode: "plan", transport: "acp" })
  await expect(page.getByRole("dialog")).toBeHidden()
  const composer = page.getByRole("textbox", { name: "Message" })
  await expect(composer).toHaveAttribute("placeholder", "Starting Codex…")
  await composer.fill("check the budgets")
  await composer.press("Enter")
  await expect(page.getByText("Sends when Codex is ready")).toBeVisible()
  // The message stays in the composer (and its saved draft) until it goes out.
  await expect(composer).toHaveValue("check the budgets")
  expect(prompts).toHaveLength(0)
  reply = [...sessions, { ...starting, conversation_state: "idle", state: terminal(true) }]
  // Without live events the list polls every 15 s.
  await page.clock.runFor(16_000)
  await expect.poll(() => prompts).toEqual([{ text: "check the budgets" }])
  await expect(page.getByText("Sends when Codex is ready")).toBeHidden()
  await expect(composer).toHaveValue("")
})

test("⌥-click on + lists the agents", async ({ page }) => {
  await page.goto("/sessions")
  await page.getByRole("button", { name: "New Codex chat in oracle on host-a" }).click({ modifiers: ["Alt"] })
  await expect(page.getByRole("menuitem", { name: "New Claude chat" })).toBeVisible()
  await expect(page).not.toHaveURL(/\/sessions\/[^/]+\/s/)
})

test("closing New session drops the folder it was opened for", async ({ page }) => {
  await page.goto("/sessions?new=1&machine=local&folder=%2Fhome%2Foleg%2Foracle")
  await expect(page.getByRole("dialog").getByRole("heading", { name: "oracle" })).toBeVisible()
  await page.keyboard.press("Escape")
  await expect(page.getByRole("dialog")).toBeHidden()
  await expect(page).not.toHaveURL(/folder=|machine=|new=/)
})

test("Refresh rereads sessions", async ({ page }) => {
  await page.goto("/sessions")
  const list = page.getByRole("navigation", { name: "Sessions" })
  await expect(list.locator("[data-source-row]")).toHaveCount(2)

  reply = renamed
  const before = fetches
  await page.getByRole("button", { name: "Refresh sessions" }).click()
  await expect.poll(() => fetches).toBeGreaterThan(before)
  await expect(page.getByRole("link", { name: /oracle main/ })).toBeVisible()

  await page.unroute("**/api/nodes")
  await page.route("**/api/nodes", (route) => route.fulfill({ status: 400, json: { error: "Tailscale is unavailable" } }))
  await page.getByRole("button", { name: "Refresh sessions" }).click()
  await expect(list.getByRole("alert")).toHaveText(/Can't look for other machines: Tailscale is unavailable/)
})

test("a reload paints the last-known list before the gateway answers", async ({ page }) => {
  await page.clock.install()
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows).toHaveCount(2)
  // The cache is written a second after the last change.
  await page.clock.runFor(1_100)
  await page.waitForFunction(() => new Promise((resolve) => {
    const open = indexedDB.open("agentpack-cache")
    open.onsuccess = () => {
      const request = open.result.transaction("queries").objectStore("queries").get("client")
      request.onsuccess = () => resolve(request.result !== undefined)
    }
  }))
  await page.route(/\/api\/sessions(\?.*)?$/, () => undefined)
  await page.reload()
  await expect(rows).toHaveCount(2)
})

test("⌘/Ctrl- and Shift-click mark rows; the menu then acts on all of them", async ({ page }) => {
  const archived: string[] = []
  await page.route("**/api/sessions/*", (route) => {
    if (route.request().method() !== "PATCH") return route.fallback()
    archived.push(new URL(route.request().url()).pathname.split("/").pop()!)
    return route.fulfill({ json: {} })
  })
  await page.route("**/api/sessions/*/stop", (route) => route.fulfill({ json: { stopped: true } }))
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await rows.first().click({ modifiers: ["ControlOrMeta"] })
  await rows.last().click({ modifiers: ["Shift"] })
  // Marking never opens a session.
  await expect(page).toHaveURL(/\/sessions(\?|$)/)
  await expect(rows.and(page.locator("[data-marked]"))).toHaveCount(2)

  await rows.last().click({ button: "right" })
  // Only s1 is running, so the stop counts one; both can be archived.
  await expect(page.getByRole("menuitem", { name: "Stop agent…" })).toBeEnabled()
  await page.getByRole("menuitem", { name: "Archive 2 sessions…" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Archive 2" }).click()
  await expect.poll(() => archived.sort()).toEqual(["s1", "s2"])
  await expect(page.locator("[data-marked]")).toHaveCount(0)
})

test("a chat's row menu switches agentpack tools, and its helpers list under it", async ({ page }) => {
  const chat = { ...sessions[0], id: "c1", name: "lead", transport: "acp", conversation_state: "idle", tools: true }
  const helper = { ...chat, id: "c2", name: "helper", parent_id: "c1", started_by: { kind: "browser", user: "oleg@", agent: "claude" } }
  reply = [helper, sessions[1], chat]
  const bodies: unknown[] = []
  await page.route("**/api/sessions/c1/tools", (route) => {
    bodies.push(route.request().postDataJSON())
    return route.fulfill({ json: { tools: false } })
  })
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows.filter({ hasText: /lead|helper/ })).toHaveText([/lead/, /helper/])
  await rows.filter({ hasText: "lead" }).click({ button: "right" })
  const toggle = page.getByRole("menuitemcheckbox", { name: "agentpack Tools" })
  await expect(toggle).toHaveAttribute("aria-checked", "true")
  await toggle.click()
  await expect.poll(() => bodies).toEqual([{ enabled: false }])
})

test("Esc clears the marked rows", async ({ page }) => {
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await rows.first().click({ modifiers: ["ControlOrMeta"] })
  await rows.last().click({ modifiers: ["ControlOrMeta"] })
  await expect(page.locator("[data-marked]")).toHaveCount(2)
  await page.keyboard.press("Escape")
  await expect(page.locator("[data-marked]")).toHaveCount(0)
})
