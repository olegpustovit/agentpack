import { expect, test, type Page } from "@playwright/test"

const identity = { node: { id: "n-self", name: "oracle-vm", os: "linux", version: "0.9.3" }, auth: "", tailscale_user: "" }
const nodes = [
  { id: "n-mac", name: "mac-studio", online: true },
  { id: "n-fed", name: "fedora-laptop", online: true },
  { id: "n-build", name: "build-box", online: false },
  { id: "n-phone", name: "iphone", online: true },
  { id: "n-ci", name: "ci-runner-3", online: true },
]

// Each peer carries address fields the page must never show. The client parser drops them, and this checks it.
const topology = {
  self: { id: "n-self", name: "oracle-vm", os: "linux", relay: "fra", addr: "100.64.0.1" },
  peers: [
    { id: "n-mac", name: "mac-studio", os: "macOS", state: "reachable", connection: "direct", relay: "", rtt_ms: 12, sessions: 0, cur_addr: "203.0.113.7:41641", ip: "100.101.102.103" },
    { id: "n-fed", name: "fedora-laptop", os: "linux", state: "reachable", connection: "relay", relay: "fra", rtt_ms: 84, sessions: 0, ip: "fd7a:115c:a1e0::53" },
    { id: "n-build", name: "build-box", os: "linux", state: "offline", connection: "", relay: "", sessions: 0, last_seen: "2026-10-27T10:00:00Z" },
    { id: "n-phone", name: "iphone", os: "iOS", state: "no-gateway", connection: "direct", relay: "", sessions: 0, cur_addr: "198.51.100.9:443" },
    { id: "n-ci", name: "ci-runner-3", os: "linux", state: "not-allowed", connection: "relay", relay: "ams", sessions: 0, ip: "100.100.1.2" },
  ],
}

const session = (id: string) => ({ id, directory: "/srv/app", harness: "claude", mode: "default", state: { interactive: false, running: true, restarting: false, rows: 24, columns: 80, exit_code: 0 } })
const sessions: Record<string, unknown[]> = {
  local: [session("s1"), session("s2")],
  "n-mac": [session("m1"), session("m2"), session("m3")],
  "n-fed": [session("f1")],
}

const ipv4 = /\b\d{1,3}(?:\.\d{1,3}){3}\b/
// IPv6 forms with "::" or eight groups. Plain "10:00:00" times do not match.
const ipv6 = /\b(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}\b|[0-9a-f]::[0-9a-f]|\b[0-9a-f]{1,4}::/i

test.beforeEach(async ({ page }) => {
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity*", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: nodes }))
  await page.route("**/api/topology", (route) => route.fulfill({ json: topology }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    const node = new URL(route.request().url()).searchParams.get("node") ?? "local"
    return route.fulfill({ json: sessions[node] ?? [] })
  })
})

function grid(page: Page) {
  return page.getByRole("table", { name: "Machines and connections" })
}

function row(page: Page, name: string) {
  return grid(page).locator("tbody tr").filter({ hasText: name })
}

test("all four peer states render with words", async ({ page }) => {
  await page.goto("/machines/topology")

  await expect(row(page, "mac-studio")).toContainText("Reachable")
  await expect(row(page, "build-box")).toContainText("Offline")
  await expect(row(page, "iphone")).toContainText("No gateway")
  await expect(row(page, "ci-runner-3")).toContainText("Not allowed")

  const unreachable = page.getByRole("heading", { name: "Not reachable by Agentpack" })
  await expect(unreachable).toBeVisible()
  await expect(unreachable.locator("xpath=following-sibling::ul")).toContainText("No gateway running on this device")
  await expect(unreachable.locator("xpath=following-sibling::ul")).toContainText("Not on the sharing allow list")
})

test("diagram labels each edge and dashes only the relay one", async ({ page }) => {
  await page.goto("/machines/topology")

  const svg = page.locator('[data-slot="topology-diagram"]')
  await expect(svg.locator("path")).toHaveCount(2)
  await expect(svg.locator("path[stroke-dasharray]")).toHaveCount(1)
  if (test.info().project.name === "phone") {
    // Phones hide the diagram by design (max-[760px]:hidden) and show the same edge labels in the grid's Path column.
    await expect(svg).toBeHidden()
    await expect(row(page, "mac-studio")).toContainText("direct")
    await expect(row(page, "fedora-laptop")).toContainText("relay fra")
  } else {
    await expect(svg.getByText(/^direct/)).toBeVisible()
    await expect(svg.getByText(/^relay fra/)).toBeVisible()
  }
  // Session counts come from the sessions queries, since the topology reports 0.
  if (test.info().project.name !== "phone") await expect(svg.getByText("macOS · Reachable · 3 sessions")).toBeVisible()
})

test("no IP address appears on the page or in the grid", async ({ page }) => {
  await page.goto("/machines/topology")
  await expect(row(page, "mac-studio")).toBeVisible()
  const html = await page.evaluate(() => document.body.outerHTML)
  expect(html).not.toMatch(ipv4)
  expect(html).not.toMatch(ipv6)
  const gridHtml = await grid(page).evaluate((table) => table.outerHTML)
  expect(gridHtml).not.toMatch(ipv4)
  expect(gridHtml).not.toMatch(ipv6)
})

test("the layout does not overflow at 390 px", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/machines/topology")
  await expect(grid(page)).toBeVisible()

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})
