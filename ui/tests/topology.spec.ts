import { expect, test, type Page } from "@playwright/test"

const identity = { node: { id: "n-self", name: "oracle-vm", os: "linux", version: "0.9.3", schema_version: 22 }, auth: "", tailscale_user: "" }
const nodes = [
  { id: "n-mac", name: "mac-studio", online: true, main: true, version: "0.9.2" },
  { id: "n-fed", name: "fedora-laptop", online: true, version: "0.9.3" },
  { id: "n-build", name: "build-box", online: false },
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

const ipv4 = /\b\d{1,3}(?:\.\d{1,3}){3}\b/
// IPv6 forms with "::" or eight groups. Plain "10:00:00" times do not match.
const ipv6 = /\b(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}\b|[0-9a-f]::[0-9a-f]|\b[0-9a-f]{1,4}::/i

test.beforeEach(async ({ page }) => {
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
  await page.route("**/api/identity*", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes*", (route) => route.fulfill({
    json: new URL(route.request().url()).searchParams.has("discover")
      ? [...nodes, { id: "n-new", name: "new-machine", online: true }, { id: "n-off", name: "offline-machine", online: false }]
      : nodes,
  }))
  await page.route("**/api/topology", (route) => route.fulfill({ json: topology }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/settings", (route) => route.fulfill({ json: { sharing_enabled: true } }))
  await page.route("**/api/version/latest", (route) => route.fulfill({ json: { version: "0.9.3" } }))
  await page.route("**/api/library/replication/peers", (route) => route.fulfill({
    json: {
      peers: [{ node: "n-mac", mesh: true }, { node: "n-fed", mesh: true }, { node: "n-build", mesh: true }],
      contacts: { "n-mac": { last_sync: "2026-10-27T10:00:00Z", schema_version: 21 } },
    },
  }))
})

function grid(page: Page) {
  return page.getByRole("table", { name: "Machines", exact: true })
}

function row(page: Page, name: string) {
  return grid(page).getByRole("row").filter({ hasText: name })
}

test("the mesh table shows versions, schema, sync state and Main", async ({ page }) => {
  await page.goto("/machines/topology")
  const mac = row(page, "mac-studio")
  await expect(mac.getByText("Main", { exact: true })).toBeVisible()
  await expect(mac).toContainText("0.9.2 · schema 21")
  await expect(mac.getByRole("button", { name: "Update", exact: true })).toBeEnabled()
  await expect(mac).toContainText("Synced")
  await expect(row(page, "build-box")).toContainText("Offline · last seen")
})

test("the diagram labels each edge and opens a machine from its node", async ({ page }) => {
  await page.goto("/machines/topology")

  const svg = page.locator('[data-slot="topology-diagram"]')
  await expect(svg.locator("path")).toHaveCount(2)
  await expect(svg.locator("path[stroke-dasharray]")).toHaveCount(1)
  await expect(svg.getByText(/^direct/)).toBeVisible()
  await expect(svg.getByText(/^relay fra/)).toBeVisible()
  await expect(svg.getByText("macOS · 0.9.2")).toBeVisible()
  await svg.getByText("mac-studio", { exact: true }).click()
  await expect(page).toHaveURL(/\/machines\/n-mac$/)
})

test("no IP address appears on the page", async ({ page }) => {
  await page.goto("/machines/topology")
  await expect(row(page, "mac-studio")).toBeVisible()
  // SVG path coordinates can resemble an IPv4 address.
  const html = (await page.evaluate(() => document.body.outerHTML)).replace(/ d="[^"]*"/g, "")
  expect(html).not.toMatch(ipv4)
  expect(html).not.toMatch(ipv6)
})

test("the layout does not overflow at 390 px", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/machines/topology")
  await expect(row(page, "mac-studio").getByRole("button", { name: "Update", exact: true })).toBeEnabled()
  await expect(page.locator('[data-slot="topology-diagram"]')).toBeHidden()
  const tableRight = await grid(page).evaluate((table) => table.getBoundingClientRect().right)
  expect(tableRight).toBeLessThanOrEqual(390)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
})

test("the old Replication link opens Topology, where Sync now targets one peer", async ({ page }) => {
  const syncs: unknown[] = []
  await page.route("**/api/library/replication/sync", (route) => {
    syncs.push(route.request().postDataJSON())
    return route.fulfill({ json: { synced: true } })
  })
  await page.goto("/settings/replication")
  await expect(page).toHaveURL(/\/machines\/topology$/)
  await row(page, "mac-studio").getByRole("button", { name: "mac-studio actions" }).click()
  await page.getByRole("menuitem", { name: "Sync now" }).click()
  await expect(page.getByText("Sync requested with mac-studio.")).toBeVisible()
  expect(syncs).toEqual([{ peer: "n-mac" }])
})

test("adding a machine confirms what it will receive", async ({ page }) => {
  const additions: unknown[] = []
  await page.route("**/api/library/replication/peers", (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    additions.push(route.request().postDataJSON())
    return route.fulfill({ json: { enrolled: true } })
  })
  await page.goto("/machines/topology")
  await expect(row(page, "offline-machine").getByRole("button", { name: "Add", exact: true })).toBeDisabled()
  await row(page, "new-machine").getByRole("button", { name: "Add", exact: true }).click()
  const dialog = page.getByRole("alertdialog", { name: "Add new-machine to this mesh?" })
  await expect(dialog).toContainText("password hashes, chat history, limits, Library data and MCP logins")
  expect(additions).toEqual([])
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect.poll(() => additions).toEqual([{ peer: "n-new" }])
})

test("removing a machine confirms and links to Vault afterward", async ({ page }) => {
  const removals: string[] = []
  await page.route("**/api/library/replication/peers/*", (route) => {
    removals.push(route.request().method())
    return route.fulfill({ json: { removed: true } })
  })
  await page.goto("/machines/topology")
  await row(page, "mac-studio").getByRole("button", { name: "mac-studio actions" }).click()
  await page.getByRole("menuitem", { name: "Remove from mesh…" }).click()
  const dialog = page.getByRole("alertdialog", { name: "Remove mac-studio from the mesh?" })
  await expect(dialog).toContainText("Both machines keep what they already have")
  expect(removals).toEqual([])
  await dialog.getByRole("button", { name: "Remove", exact: true }).click()
  await expect(page.getByText(/Sign out of OAuth logins in Vault/)).toBeVisible()
  await expect(page.getByRole("link", { name: "Open Vault" })).toHaveAttribute("href", "/settings/connections")
  expect(removals).toEqual(["DELETE"])
})

test("when Tailscale sharing is off, Topology links to Sharing", async ({ page }) => {
  await page.route("**/api/settings", (route) => route.fulfill({ json: { sharing_enabled: false } }))
  await page.goto("/machines/topology")
  await expect(page.getByText("Turn on Tailscale sharing to add machines.")).toBeVisible()
  await expect(page.getByRole("link", { name: "Open Sharing" })).toHaveAttribute("href", "/settings/sharing")
  await expect(row(page, "new-machine").getByRole("button", { name: "Add", exact: true })).toBeDisabled()
})
