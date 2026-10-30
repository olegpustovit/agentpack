import { expect, test } from "@playwright/test"

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
  reply = sessions
  fetches = 0
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
  // Aborting the stream makes the list poll on its 15 s fallback, as it does without live events.
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    fetches += 1
    return route.fulfill({ json: reply })
  })
})

test("the list keeps its DOM for 60 s of unchanged data", async ({ page }) => {
  test.setTimeout(120_000)
  await page.goto("/sessions")
  const list = page.getByRole("navigation", { name: "Sessions" })
  await expect(list.getByRole("link", { name: /build/ })).toBeVisible()

  const polls = fetches
  await page.evaluate(() => {
    const nav = document.querySelector('nav[aria-label="Sessions"]')
    if (!nav) throw new Error("sessions list not found")
    const counter = window as Window & { churn?: number }
    counter.churn = 0
    new MutationObserver((records) => {
      counter.churn = (counter.churn ?? 0) + records.length
    }).observe(nav, { subtree: true, childList: true, attributes: true, characterData: true })
  })

  await page.waitForTimeout(60_000)
  // The list must have polled during the window, or the zero count proves nothing.
  expect(fetches - polls).toBeGreaterThanOrEqual(3)
  expect(await page.evaluate(() => (window as Window & { churn?: number }).churn)).toBe(0)
})

test("focus on a row survives a refetch that changes the data", async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows).toHaveCount(2)
  await rows.first().focus()

  reply = renamed
  const polls = fetches
  await expect.poll(() => fetches, { timeout: 25_000 }).toBeGreaterThan(polls)
  await expect(page.getByRole("link", { name: /oracle main/ })).toBeVisible()
  await expect(rows.first()).toBeFocused()
})

test("holding an arrow key opens the session once, 150 ms after the last press", async ({ page }) => {
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
  await page.getByRole("combobox", { name: "Agent" }).click()
  await page.getByRole("option", { name: "Cursor CLI" }).click()
  const posted = page.waitForRequest((request) => request.method() === "POST" && /\/api\/sessions$/.test(request.url()))
  await page.getByRole("button", { name: "Start session" }).click()
  const body = (await posted).postDataJSON()
  expect(body).toMatchObject({ directory: "/home/oleg/agentpack", harness: "agent" })
})
