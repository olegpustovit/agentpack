import { expect, test } from "@playwright/test"

const local = { node: { id: "n1", name: "host-a", os: "linux", version: "0.9.0" }, auth: "password", tailscale_user: "" }
const peer = { node: { id: "n2", name: "oracle-vm", os: "linux", version: "0.9.1" }, auth: "", tailscale_user: "" }
const nodes = [
  { id: "n2", name: "oracle-vm", online: true },
  { id: "n3", name: "lab-pi", online: false },
]
const topology = {
  self: { id: "n1", name: "host-a", os: "linux", relay: "" },
  peers: [{ id: "n2", name: "oracle-vm", os: "linux", state: "active", connection: "direct", relay: "", rtt_ms: 14, version: "0.9.1", sessions: 1 }],
}

const session = (id: string, directory: string) => ({
  id,
  directory,
  harness: "claude",
  mode: "default",
  state: { interactive: true, running: true, restarting: false, rows: 24, columns: 80, exit_code: 0 },
})
const sessions: Record<string, unknown[]> = {
  local: [session("s1", "/home/oleg/agentpack")],
  n2: [session("s2", "/srv/oracle")],
}

const machineInfo = {
  version: "0.9.1", os: "linux", arch: "arm64", cores: 8, memory: 31 * 1024 ** 3, disk_free: 120 * 1024 ** 3, disk_total: 200 * 1024 ** 3,
  agents: [{ harness: "claude", installed: true, version: "2.1.140" }, { harness: "cursor", installed: false }],
}

test.beforeEach(async ({ page }) => {
  await page.route(/\/api\/machine(\?.*)?$/, (route) => route.fulfill({ json: machineInfo }))
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
  await page.route(/\/api\/identity(\?.*)?$/, (route) => route.fulfill({ json: new URL(route.request().url()).searchParams.get("node") === "n2" ? peer : local }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: nodes }))
  await page.route("**/api/topology", (route) => route.fulfill({ json: topology }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: Object.entries(sessions).flatMap(([node_id, rows]) => rows.map((row) => ({ ...row as object, node_id: node_id === "local" ? local.node.id : node_id }))) }))
})

test("machine detail shows its agents and system", async ({ page }) => {
  await page.goto("/machines/n2")

  await expect(page.getByText("Online · direct, 14 ms · 1 session running")).toBeVisible()
  const agents = page.getByRole("table", { name: "Agents" })
  await expect(agents.locator("tbody tr").filter({ hasText: "Claude" })).toContainText("2.1.140")
  await expect(agents.locator("tbody tr").filter({ hasText: "Cursor" })).toContainText("Not installed")
  await expect(page.getByText("linux arm64 · 8 cores · 31 GB")).toBeVisible()
})

test("the machine lists its worktrees and removes one after naming what it discards", async ({ page }) => {
  const worktree = (branch: string, patch: object) => ({ path: `/h/worktrees/0123456789abcdef/${branch}`, main: "/srv/oracle", branch: `ap/${branch}`, head: "abc", dirty: 0, untracked: 0, unpushed: 0, merged: false, orphaned: false, sessions: [], running: 0, size: 5 * 1024 ** 2, ...patch })
  const removed: unknown[] = []
  await page.route(/\/api\/worktrees(\?.*)?$/, (route) => route.fulfill({ json: { git: true, worktrees: [worktree("cache", { dirty: 2, untracked: 1, unpushed: 2 }), worktree("done", { merged: true })] } }))
  await page.route(/\/api\/worktrees\/remove(\?.*)?$/, (route) => {
    removed.push({ url: route.request().url(), body: route.request().postDataJSON() })
    return route.fulfill({ json: { removed: true } })
  })
  await page.goto("/machines/n2")

  const rows = page.getByRole("table", { name: "Worktrees" }).locator("tbody tr")
  await expect(rows.nth(0)).toContainText("3 uncommitted files · 2 commits not pushed")
  await expect(rows.nth(1)).toContainText("Merged")
  await rows.nth(0).getByRole("button", { name: "Remove…" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("Discard 3 uncommitted files?")
  await confirm.getByRole("button", { name: "Discard and remove" }).click()
  await expect.poll(() => removed).toEqual([{ url: expect.stringContaining("node=n2"), body: { path: "/h/worktrees/0123456789abcdef/cache", discard: true } }])
})

test("an offline machine says so in a banner", async ({ page }) => {
  await page.goto("/machines/n3")

  await expect(page.getByText("lab-pi is offline.")).toBeVisible()
})

test("the Topology toolbar summarizes reachable and offline machines", async ({ page }) => {
  await page.goto("/machines/topology")
  await expect(page.locator('[data-slot="toolbar"]')).toContainText("1 reachable · 1 offline")
})
