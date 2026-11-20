import { expect, test, type Locator, type Page } from "@playwright/test"
import { mockAPI, projectRow } from "./helpers"

// Status-bar assertions need desktop width: the status bar is hidden under 760px.
// Machines, Limits, Settings and startup ports of gateway.spec.js.

function machineRow(page: Page, name: string): Locator {
  return page
    .getByRole("table", { name: "Projects", exact: true })
    .locator("tbody tr")
    .filter({ has: page.getByText(name, { exact: true }) })
}

const projectsPath = /\/api\/projects(\?.*)?$/
const limitsPath = /\/api\/limits(\?.*)?$/
const nodesPath = /\/api\/nodes(\?.*)?$/
const settingsPath = /\/api\/settings(\?.*)?$/

// Moved from Recent folders to Machines: forgetting a folder removes it from that machine's list only.
test("forgetting a recent folder does not stop a session", async ({ page }) => {
  const requests = await mockAPI(page)
  await page.route(projectsPath, (route) =>
    route.fulfill({ json: requests.some((request) => request.method === "DELETE") ? [] : [projectRow("/work/previous", [{ harness: "codex" }])] }),
  )
  await page.goto("/machines/local")

  await machineRow(page, "previous").click({ button: "right" })
  await page.getByRole("menuitem", { name: "Forget", exact: true }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("Forget previous?")
  await confirm.getByRole("button", { name: "Forget", exact: true }).click()

  await expect(page.getByText("No projects yet")).toBeVisible()
  expect(requests.filter((request) => request.method === "DELETE").map((request) => [request.path, request.body])).toEqual([
    ["/api/recent", { directory: "/work/previous" }],
  ])
  expect(requests.some((request) => request.path.endsWith("/stop"))).toBeFalsy()
})

// Moved from Recent folders to Machines: unavailable folders keep their reason and cannot start a session.
test("an agent signs out from the machine page after a confirmation", async ({ page }) => {
  await mockAPI(page)
  let signedIn = true
  let loggedOut = 0
  await page.route("**/api/agents", (route) =>
    route.fulfill({ json: { agents: [{ harness: "codex", installed: true, signed_in: signedIn, label: signedIn ? "dev@example.com" : undefined, sign_out: signedIn, methods: [{ id: "chatgpt", label: "ChatGPT account" }] }] } }),
  )
  await page.route("**/api/agents/codex/logout", (route) => {
    loggedOut++
    signedIn = false
    return route.fulfill({ json: { signed_out: true } })
  })
  await page.goto("/machines/local")
  await page.getByRole("button", { name: "Sign out…" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("Sign Codex out on Gateway machine?")
  await confirm.getByRole("button", { name: "Sign out" }).click()
  await expect(page.getByText("Not signed in")).toBeVisible()
  expect(loggedOut).toBe(1)
})

test("recent folders retain unavailable reasons and only available paths can start", async ({ page }) => {
  await mockAPI(page)
  await page.route(projectsPath, (route) =>
    route.fulfill({
      json: [
        projectRow("/work/available", [{ harness: "codex" }]),
        projectRow("/work/missing", [{ harness: "codex" }], { available: false, unavailable_reason: "Folder is missing or inaccessible" }),
        projectRow("/work/replaced", [{ harness: "claude" }], { available: false, unavailable_reason: "Folder changed since its last local launch" }),
      ],
    }),
  )
  await page.goto("/machines/local")

  await expect(page.getByText("Folder is missing or inaccessible")).toBeVisible()
  await expect(page.getByText("Folder changed since its last local launch")).toBeVisible()
  for (const [name, usable] of [
    ["available", true],
    ["missing", false],
    ["replaced", false],
  ] as const) {
    await machineRow(page, name).click({ button: "right" })
    const start = page.getByRole("menuitem", { name: "New session…" })
    if (usable) await expect(start).toBeEnabled()
    else await expect(start).toBeDisabled()
    await page.keyboard.press("Escape")
    await expect(page.getByRole("menu")).toBeHidden()
  }
})

test("limits view shows each harness with partial failures and no raw CLI output", async ({ page }) => {
  await mockAPI(page)
  await page.goto("/")
  await page.getByRole("link", { name: "Usage", exact: true }).click()
  await page.getByRole("navigation", { name: "Usage" }).getByRole("link", { name: /^Limits/ }).click()

  const grid = page.getByRole("region", { name: "Provider limits" })
  await expect(grid).toBeVisible()
  await expect(page.getByRole("meter", { name: "Codex 5h remaining" })).toHaveAttribute("value", "72")
  await expect(grid.locator('[data-provider="grok"]')).toContainText("Signed out")
  await expect(grid.locator('[data-provider="codex"]')).toContainText("Codex")
})

test("limits shortcut opens the view and a passed reset is labelled refreshing", async ({ page }) => {
  const past = new Date(Date.now() - 60000).toISOString()
  await mockAPI(page)
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.route(limitsPath, (route) =>
    route.fulfill({
      json: { limits: [{ harness: "claude", state: "available", windows: [{ id: "claude/session", label: "Session", remaining_percent: 0, reset_at: past }] }] },
    }),
  )
  await page.goto("/")
  // The shell binds ⌘4 through metaKey only, so Meta is the key on every platform.
  await expect(page.getByRole("link", { name: "Usage", exact: true })).toBeVisible()
  await page.keyboard.press("Meta+4")
  await page.getByRole("navigation", { name: "Usage" }).getByRole("link", { name: /^Limits/ }).click()

  const grid = page.getByRole("region", { name: "Provider limits" })
  await expect(grid).toBeVisible()
  const reset = grid.getByText("refreshing…")
  await expect(reset).toBeVisible()
  // A passed reset is dimmed: its text colour differs from the window label on the same row.
  const label = grid.getByText("Session", { exact: true })
  expect(await reset.evaluate((element) => getComputedStyle(element).color)).not.toBe(await label.evaluate((element) => getComputedStyle(element).color))
})

test("limits automatically prefers capable nodes and refreshes every source", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.route(nodesPath, (route) =>
    route.fulfill({
      json: [
        { id: "peer-1", name: "Mac Studio", online: true },
        { id: "peer-2", name: "Offline Mac", online: false },
        { id: "peer-3", name: "Unavailable node", online: true },
      ],
    }),
  )
  const calls: { node: string | null; method: string }[] = []
  await page.route(/\/api\/limits(\/refresh)?(\?.*)?$/, (route) => {
    const node = new URL(route.request().url()).searchParams.get("node")
    calls.push({ node, method: route.request().method() })
    if (node === "peer-3") return route.fulfill({ status: 502, body: "Node is unreachable" })
    return route.fulfill({
      json: {
        limits: ["local", "peer-1"].flatMap((node_id) => {
          const remote = node_id === "peer-1"
          return [
            {
              node_id,
              harness: "codex",
              state: remote ? "available" : "unsupported",
              plan: remote ? "Pro" : undefined,
              source: "codex app-server account/rateLimits/read",
              observed_at: new Date().toISOString(),
              error: remote ? undefined : "codex CLI not found",
              windows: remote
                ? [
                    { id: "primary", label: "Session", used_percent: 28, remaining_percent: 72, duration_seconds: 18000, reset_at: new Date(Date.now() + 7200000).toISOString() },
                    { id: "weekly", label: "Weekly", used_percent: 64, remaining_percent: 36, reset_text: "Monday at 09:00" },
                    { id: "unknown", label: "Additional usage", reset_text: "Not reported" },
                  ]
                : [],
            },
            {
              node_id,
              harness: "claude",
              state: remote ? "error" : "available",
              source: "claude -p /usage",
              error: remote ? "Sign in required" : undefined,
              windows: remote ? [] : [{ id: "session", label: "Current session", remaining_percent: 81, used_percent: 19 }],
            },
          ]
        }),
      },
    })
  })

  await page.goto("/")
  // Four machines discovered: this one, two peers and the offline one.
  await expect(page.getByRole("contentinfo").getByRole("button", { name: "Connected · 4 machines · 1 offline" })).toBeVisible()
  await page.getByRole("link", { name: "Usage", exact: true }).click()
  await page.getByRole("navigation", { name: "Usage" }).getByRole("link", { name: /^Limits/ }).click()

  const grid = page.getByRole("region", { name: "Provider limits" })
  const codex = grid.locator('[data-provider="codex"]')
  await expect(codex.locator("header")).toContainText("Mac Studio")
  await expect(codex.locator("header")).toContainText("Pro")
  await expect(page.getByRole("meter", { name: "Session remaining", exact: true })).toHaveAttribute("value", "72")
  await expect(page.getByRole("meter", { name: "Weekly remaining" })).toHaveAttribute("value", "36")
  await expect(codex.getByText("Unknown", { exact: true })).toBeVisible()
  await expect(grid.getByRole("meter")).toHaveCount(3)
  await expect(grid.locator('[data-provider="claude"]').locator("header")).toContainText("Gateway machine")
  expect(calls.some((call) => call.node === "peer-2")).toBeFalsy()

  // A machine without the CLI says nothing about the account, so it is not offered as another report.
  await expect(codex.getByRole("button", { name: "Other machines (1)" })).toHaveCount(0)
  // Reads stay local; a peer's answer to the opening nudge may refetch that local read once more.
  expect(new Set(calls.filter((call) => call.method === "GET").map((call) => call.node))).toEqual(new Set([null]))

  await page.getByRole("button", { name: "Refresh", exact: true }).click()
  // Opening Limits already nudged each online peer once; Refresh asks every machine.
  await expect.poll(() => calls.filter((call) => call.method === "POST").map((call) => call.node).sort()).toEqual([null, "peer-1", "peer-1", "peer-3", "peer-3"].sort())
  await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeEnabled()

  await page.setViewportSize({ width: 390, height: 844 })
  // The grid scrolls inside its own wrapper; the page itself must not widen past the phone viewport.
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
})

test("limits labels replicated reports after machine discovery", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.route(nodesPath, async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 800))
    await route.fulfill({ json: [{ id: "peer-1", name: "Kyiv grid", online: true }] })
  })
  await page.route(limitsPath, (route) => {
    return route.fulfill({
      json: {
        limits: [
          {
            node_id: "peer-1",
            harness: "codex",
            state: "available",
            windows: [{ id: "weekly", label: "Weekly", remaining_percent: 63 }],
          },
        ],
      },
    })
  })

  await page.goto("/")
  await page.getByRole("link", { name: "Usage", exact: true }).click()
  await page.getByRole("navigation", { name: "Usage" }).getByRole("link", { name: /^Limits/ }).click()
  await expect(page.getByRole("contentinfo").getByRole("button", { name: "Connected · 2 machines" })).toBeVisible()
  await expect(page.getByRole("meter", { name: "Weekly remaining" })).toHaveAttribute("value", "63")
  await expect(page.locator('[data-provider="codex"]').locator("header")).toContainText("Kyiv grid")
})

test("startup and machine discovery announce pending work", async ({ page }) => {
  await mockAPI(page)
  let releaseStatus!: () => void
  let releaseNodes!: () => void
  const statusReady = new Promise<void>((resolve) => (releaseStatus = resolve))
  const nodesReady = new Promise<void>((resolve) => (releaseNodes = resolve))
  await page.route(/\/api\/status(\?.*)?$/, async (route) => {
    await statusReady
    await route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } })
  })
  await page.route(nodesPath, async (route) => {
    await nodesReady
    await route.fulfill({ json: [] })
  })

  await page.goto("/")
  await expect(page.getByText("Connecting to gateway…")).toBeVisible()
  releaseStatus()
  await expect(page.getByText("Discovering machines…")).toBeVisible()

  await page.getByRole("link", { name: "Machines", exact: true }).click()
  await expect(page.getByText("Discovering connected machines")).toBeVisible()
  await expect(page.locator('[aria-label="Machines"]')).toHaveAttribute("aria-busy", "true")
  releaseNodes()
  await expect(page.getByText("Discovering connected machines")).toBeHidden()
  await expect(page.locator('[aria-label="Machines"]')).toHaveAttribute("aria-busy", "false")
})

test("settings errors finish loading and offer a working retry", async ({ page }) => {
  await mockAPI(page)
  let releaseSettings!: () => void
  let fail = true
  const ready = new Promise<void>((resolve) => (releaseSettings = resolve))
  await page.route(settingsPath, async (route) => {
    await ready
    if (fail) return route.fulfill({ status: 502, body: "Gateway is unavailable" })
    return route.fulfill({ json: { running: true, platform: "macOS", sharing_enabled: true, allowed_peers: [] } })
  })

  await page.goto("/")
  await page.getByRole("link", { name: "Settings", exact: true }).first().click()
  const skeleton = page.locator('[data-slot="skeleton"]')
  await expect(skeleton.first()).toBeVisible()

  releaseSettings()
  await expect(page.getByText("Couldn't load gateway details: Gateway is unavailable")).toBeVisible({ timeout: 15_000 })
  await expect(skeleton).toHaveCount(0)

  fail = false
  await page.getByRole("button", { name: "Retry", exact: true }).click()
  await expect(page.getByText("macOS", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Retry", exact: true })).toBeHidden()
})

test("Cursor CLI is included with separate usage buckets", async ({ page }) => {
  await mockAPI(page)
  await page.route(limitsPath, (route) =>
    route.fulfill({
      json: {
        limits: [
          {
            harness: "cursor",
            state: "available",
            source: "Cursor DashboardService/GetCurrentPeriodUsage",
            windows: [
              { id: "cursor/included", label: "Included usage", used_percent: 25, remaining_percent: 75 },
              { id: "cursor/auto", label: "Auto models", used_percent: 40, remaining_percent: 60 },
              { id: "cursor/api", label: "API models", used_percent: 0, remaining_percent: 100 },
              { id: "cursor/on-demand", label: "On-demand spending limit", used_percent: 0, remaining_percent: 100 },
            ],
          },
        ],
      },
    }),
  )
  await page.goto("/")
  await page.getByRole("link", { name: "Usage", exact: true }).click()
  await page.getByRole("navigation", { name: "Usage" }).getByRole("link", { name: /^Limits/ }).click()

  await expect(page.getByRole("heading", { name: "Cursor CLI", exact: true })).toBeVisible()
  await expect(page.getByRole("meter", { name: "Auto models remaining" })).toHaveAttribute("value", "60")
  await expect(page.getByRole("meter", { name: "API models remaining" })).toHaveAttribute("value", "100")
  await expect(page.getByRole("meter")).toHaveCount(4)
})
