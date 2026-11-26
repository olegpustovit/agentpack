import { expect, test, type Page } from "@playwright/test"
import { mockAPI } from "./helpers"

test.use({ timezoneId: "UTC" })
const NOW = new Date("2026-11-18T15:00:00Z")

const hour = (iso: string) => Date.parse(iso) / 1000
const usageFixture = {
  hourly: [
    {
      hour: hour("2026-11-16T10:00:00Z"),
      node_id: "gateway",
      harness: "claude",
      folder_id: "f-web",
      model: "claude-opus-5",
      turns: 3,
      sessions: 1,
      input_tokens: 100,
      output_tokens: 900_000,
      cache_read_tokens: 2_000_000,
      cache_write_tokens: 50_000,
      cost_micros: 2_500_000,
      agent_ms: 900_000,
    },
    {
      hour: hour("2026-11-17T11:00:00Z"),
      node_id: "peer-1",
      harness: "codex",
      folder_id: "f-api",
      model: "gpt-6",
      turns: 2,
      sessions: 1,
      input_tokens: 50,
      output_tokens: 600_000,
      cache_read_tokens: 1_000_000,
      cache_write_tokens: 0,
      cost_micros: -1,
      agent_ms: 600_000,
    },
  ],
  sessions: [
    {
      id: "s1",
      node_id: "gateway",
      title: "Fix usage layout",
      name: "",
      directory: "/srv/web",
      folder_id: "f-web",
      harness: "claude",
      model: "claude-opus-5",
      turns: 3,
      input_tokens: 100,
      output_tokens: 900_000,
      cache_read_tokens: 2_000_000,
      cache_write_tokens: 50_000,
      cost_micros: 2_500_000,
      started_ms: Date.parse("2026-11-16T09:00:00Z"),
      last_event_ms: Date.parse("2026-11-16T11:00:00Z"),
    },
    {
      id: "s2",
      node_id: "peer-1",
      title: "Refactor limits",
      name: "",
      directory: "/srv/api",
      folder_id: "f-api",
      harness: "codex",
      model: "gpt-6",
      turns: 2,
      input_tokens: 50,
      output_tokens: 600_000,
      cache_read_tokens: 1_000_000,
      cache_write_tokens: 0,
      cost_micros: -1,
      started_ms: Date.parse("2026-11-17T10:00:00Z"),
      last_event_ms: Date.parse("2026-11-17T12:00:00Z"),
    },
  ],
  samples: [],
}

async function setupUsage(page: Page, override?: { usage?: object; usageStatus?: number; nodes?: object[]; topology?: object }) {
  await page.clock.setFixedTime(NOW)
  await mockAPI(page)
  await page.route("**/api/nodes*", (route) => route.fulfill({
    json: override?.nodes ?? [
      { id: "peer-1", name: "birch", online: true },
      { id: "peer-2", name: "cedar", online: false },
    ],
  }))
  await page.route("**/api/topology", (route) => route.fulfill({
    json: override?.topology ?? {
      self: { id: "gateway", name: "atlas", os: "linux", relay: "" },
      peers: [
        { id: "peer-1", name: "birch", os: "linux", state: "reachable", connection: "direct", relay: "", sessions: 0 },
        { id: "peer-2", name: "cedar", os: "linux", state: "offline", connection: "", relay: "", last_seen: "2026-11-15T12:00:00Z", sessions: 0 },
      ],
    },
  }))
  await page.route("**/api/usage?**", (route) => {
    if (override?.usageStatus && override.usageStatus >= 400) {
      return route.fulfill({ status: override.usageStatus, body: "usage store failed" })
    }
    return route.fulfill({ json: override?.usage ?? usageFixture })
  })
}

test("USE-01 at 320 px keeps Range, Metric, and Scope reachable without horizontal scroll", async ({ page }) => {
  await setupUsage(page)
  await page.setViewportSize({ width: 320, height: 640 })
  await page.goto("/usage")

  await expect(page.getByText("Output tokens · This Week")).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)

  await page.getByRole("combobox", { name: "Metric" }).click()
  await page.getByRole("option", { name: "Reported cost" }).click()
  await expect(page.getByText("Reported cost · This Week")).toBeVisible()
  await expect(page.getByRole("button", { name: "Reset" })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test("USE-02 provides focusable heatmap buttons, Show as table, and keeps hover separate from Sessions filter", async ({ page }) => {
  await setupUsage(page)
  await page.goto("/usage")

  const sessionsTable = page.getByRole("table", { name: "Sessions in this range" })
  await expect(sessionsTable.getByRole("row")).toHaveCount(3)

  await page.locator('[data-bucket="1"]').hover()
  // Hover previews in hero but never filters Sessions.
  await expect(sessionsTable.getByRole("row")).toHaveCount(3)

  // Focusable heatmap cell with aria-label containing value.
  const heatCell = page.getByRole("button", { name: /Mon 16 · .* · 900K/ })
  await expect(heatCell).toBeVisible()
  await heatCell.focus()
  await expect(heatCell).toBeFocused()

  // Show as table lists every day in the week.
  await page.getByRole("button", { name: "Show as table" }).click()
  const dayTable = page.getByRole("table", { name: "Output tokens by day" })
  await expect(dayTable.locator("tbody tr")).toHaveCount(7)

  // Filtering shows "Filtered to …" chip with Clear above the chart/table.
  await dayTable.locator("tbody tr").nth(1).getByRole("button", { name: "Filter" }).click()
  await expect(page.getByText(/Filtered to Tuesday, Nov 17/)).toBeVisible()
  await expect(sessionsTable.getByRole("row")).toHaveCount(2)
})

test("USE-04 hides numbers on error with Retry, shows Partial offline notice, and names empty range", async ({ page }) => {
  await setupUsage(page, { usageStatus: 500 })
  await page.goto("/usage")

  await expect(page.getByText(/Unavailable/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Retry" })).toBeVisible()
  await expect(page.locator('[data-slot="usage-hero"]')).toHaveCount(0)

  await setupUsage(page, { usage: { hourly: [], sessions: [], samples: [] } })
  await page.goto("/usage")
  await expect(page.getByText(/Partial: cedar offline since Nov 15/)).toBeVisible()
  await expect(page.getByText("No synced usage in this range")).toBeVisible()

  await setupUsage(page, { usage: { hourly: [], sessions: [], samples: [], coverage: [
    { node_id: "gateway", state: "current" },
    { node_id: "peer-1", state: "stale", last_sync: "2026-11-15T12:00:00Z" },
  ] } })
  await page.goto("/usage")
  await expect(page.getByText("Partial: birch sync delayed (last synced Nov 15)")).toBeVisible()

  await setupUsage(page, { usage: { hourly: [], sessions: [], samples: [], coverage: [
    { node_id: "gateway", state: "current" },
  ] } })
  await page.goto("/usage")
  await expect(page.getByText("No usage in this range")).toBeVisible()
})

test("USE-05 and USE-06 show disagreement banner, stale passed reset, remaining hero, 44px disclosure, and one sign-in link", async ({ page }) => {
  await page.clock.setFixedTime(NOW)
  await mockAPI(page)
  await page.route("**/api/nodes*", (route) => route.fulfill({
    json: [
      { id: "n2", name: "birch", online: true },
      { id: "n3", name: "cedar", online: true },
    ],
  }))
  await page.route("**/api/usage?**", (route) => route.fulfill({ json: { hourly: [], sessions: [], samples: [] } }))
  await page.route("**/api/limits**", (route) => route.fulfill({
    json: {
      limits: [
        {
          node_id: "gateway",
          harness: "codex",
          state: "available",
          observed_at: "2026-11-18T14:10:00Z",
          windows: [{ id: "session", label: "5h window", remaining_percent: 75, reset_at: "2026-11-18T17:00:00Z", duration_seconds: 18000 }],
        },
        {
          node_id: "n2",
          harness: "codex",
          state: "available",
          observed_at: "2026-11-18T14:59:00Z",
          windows: [{ id: "session", label: "5h window", remaining_percent: 4, reset_at: "2026-11-18T17:00:00Z", duration_seconds: 18000 }],
        },
        {
          node_id: "n3",
          harness: "claude",
          state: "available",
          observed_at: "2026-11-18T14:00:00Z",
          windows: [{ id: "session", label: "Expired window", remaining_percent: 50, reset_at: "2026-11-18T14:30:00Z", duration_seconds: 18000 }],
        },
        {
          node_id: "n2",
          harness: "grok",
          state: "error",
          error: "authentication required",
          observed_at: "2026-11-18T14:59:00Z",
          windows: [],
        },
        {
          node_id: "n3",
          harness: "grok",
          state: "error",
          error: "authentication required",
          observed_at: "2026-11-18T14:00:00Z",
          windows: [],
        },
      ],
    },
  }))

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/usage/limits")

  // Hero leads with Remaining % left, not Used.
  const tightest = page.getByRole("region", { name: "Tightest limit" })
  await expect(tightest).toContainText("Remaining")
  await expect(tightest).toContainText("4% left")
  await expect(tightest).toContainText("Estimate ·")

  // Disagreement banner names both machines.
  await expect(page.getByText(/birch reports 4% left.*reported 75%/)).toBeVisible()

  // Stale reset passed window says so without a projection.
  await expect(page.getByText("Stale · reset passed")).toBeVisible()

  // Other machines disclosure is >= 44px tall on phone.
  const disclosure = page.getByRole("button", { name: "Other machines (1)" })
  expect((await disclosure.boundingBox())!.height).toBeGreaterThanOrEqual(44)

  // A provider signed out everywhere is one row that links to Vault, with no disclosure.
  await expect(page.locator('[data-provider="grok"]').getByRole("button")).toHaveCount(0)
  const signInLink = page.getByRole("link", { name: "Sign in on birch" })
  await expect(signInLink).toBeVisible()
  await expect(signInLink).toHaveAttribute("href", "/settings/connections")
})
