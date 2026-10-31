import { expect, test, type Page } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0.9.0" }, auth: "password", tailscale_user: "" }
const nodes = [
  { id: "n2", name: "Fast Mac", online: true },
  { id: "n3", name: "Slow Mac", online: true },
  { id: "n4", name: "Offline Mac", online: false },
]

const soon = new Date(Date.now() + 2 * 3_600_000).toISOString()
const codex = (remaining: number) => ({
  harness: "codex",
  state: "available",
  plan: "Pro",
  windows: [{ id: "session", label: "Session", remaining_percent: remaining, reset_at: soon }],
})

// Each machine answers with its own limits. `node` selects the machine; the gateway has none.
let limits: Record<string, unknown[]> = {}
// Refresh responses, when they differ from the GET response.
let refreshed: Record<string, unknown[]> = {}
// A held refresh response for a machine, released by the test.
let gates: Record<string, Promise<void>> = {}

function machineOf(url: string): string {
  return new URL(url).searchParams.get("node") ?? "local"
}

test.beforeEach(async ({ page }) => {
  limits = {
    local: [codex(72), { harness: "grok", state: "error", error: "grok billing: authentication required", windows: [] }],
    n2: [{ harness: "codex", state: "available", plan: "Max", windows: [{ id: "weekly", label: "Peer weekly", remaining_percent: 50 }] }],
    n3: [{ harness: "claude", state: "available", windows: [{ id: "session", label: "Claude session", remaining_percent: 80 }] }],
  }
  refreshed = {}
  gates = {}
  await page.route("**/api/**", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: nodes }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route("**/api/limits**", async (route) => {
    const machine = machineOf(route.request().url())
    if (route.request().url().includes("/refresh")) {
      await gates[machine]
      return route.fulfill({ json: { limits: refreshed[machine] ?? limits[machine] ?? [] } })
    }
    return route.fulfill({ json: { limits: limits[machine] ?? [] } })
  })
})

function limitRequests(page: Page): string[] {
  const requests: string[] = []
  page.on("request", (request) => {
    const url = new URL(request.url())
    if (url.pathname === "/api/limits") requests.push(url.searchParams.get("node") ?? "local")
  })
  return requests
}

const grid = (page: Page) => page.getByRole("table", { name: "Provider limits" })

test("Refresh shows each machine's response before the slowest machine answers", async ({ page }) => {
  let releaseSlow = () => {}
  gates.n3 = new Promise<void>((resolve) => (releaseSlow = resolve))
  refreshed = { local: [codex(61)], n3: [{ harness: "claude", state: "available", windows: [{ id: "session", label: "Claude session", remaining_percent: 5 }] }] }

  await page.goto("/limits")
  await expect(grid(page).getByText("72% left")).toBeVisible()

  await page.getByRole("button", { name: "Refresh" }).click()
  await expect(grid(page).getByText("61% left")).toBeVisible()
  await expect(grid(page).getByText("80% left")).toBeVisible()
  await expect(grid(page).getByText("5% left")).toHaveCount(0)

  releaseSlow()
  await expect(grid(page).getByText("5% left")).toBeVisible()
  await expect(page.getByRole("button", { name: "Refresh" })).toBeEnabled()
})

test("returning to Limits within a minute makes no request and shows no loader", async ({ page }) => {
  const requests = limitRequests(page)
  await page.goto("/limits")
  await expect(grid(page).getByText("72% left")).toBeVisible()
  const before = requests.length

  // Views on desktop, the tab bar on phone: the link is the only visible one with this name.
  await page.getByRole("link", { name: "Sessions", exact: true }).click()
  await expect(page).toHaveURL(/\/sessions/)
  await page.getByRole("link", { name: "Limits", exact: true }).click()

  await expect(grid(page).getByText("72% left")).toBeVisible()
  await expect(page.locator('[data-slot="skeleton"]')).toHaveCount(0)
  expect(requests.length).toBe(before)
  expect(requests).not.toContain("n4")
})

test("unavailable providers sit in a dim group below the usable ones", async ({ page }) => {
  await page.goto("/limits")
  await expect(grid(page).getByText("grok billing: authentication required")).toBeVisible()

  const providers = await grid(page).locator("tbody").evaluateAll((bodies) => bodies.map((body) => body.dataset.provider))
  expect(providers).toEqual(["codex", "claude", "grok"])

  const colors = await grid(page)
    .locator("tbody")
    .evaluateAll((bodies) => bodies.map((body) => getComputedStyle(body).color))
  expect(colors[2]).not.toBe(colors[0])
})

test("rows from other machines sit under a disclosure", async ({ page }) => {
  await page.goto("/limits")
  await expect(grid(page).getByText("72% left")).toBeVisible()
  await expect(grid(page).getByText("Peer weekly", { exact: true })).toHaveCount(0)

  const disclosure = grid(page).getByRole("button", { name: "Other machines (1)" })
  await expect(disclosure).toHaveAttribute("aria-expanded", "false")
  await disclosure.click()
  await expect(disclosure).toHaveAttribute("aria-expanded", "true")
  await expect(grid(page).getByText("Peer weekly", { exact: true })).toBeVisible()
  await expect(grid(page).getByRole("cell", { name: "Fast Mac", exact: true })).toBeVisible()
})

test("the status bar shows the last limits update on Limits and nothing on Sessions", async ({ page }) => {
  await page.goto("/limits")
  const status = page.getByRole("contentinfo", { includeHidden: true })
  await expect(status).toContainText(/Updated (now|\d+ seconds? ago|\d+ minutes? ago)/)

  await page.getByRole("link", { name: "Sessions", exact: true }).click()
  await expect(page).toHaveURL(/\/sessions/)
  await expect(status).not.toContainText("Updated")
})
