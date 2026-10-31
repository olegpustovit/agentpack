import { expect, test, type Page } from "@playwright/test"

const local = { node: { id: "n1", name: "host-a", os: "linux", version: "0.9.0" }, auth: "password", tailscale_user: "" }
const peer = { node: { id: "n2", name: "oracle-vm", os: "linux", version: "0.9.1" }, auth: "", tailscale_user: "" }
const nodes = [
  { id: "n2", name: "oracle-vm", online: true },
  { id: "n3", name: "lab-pi", online: false },
]
const topology = {
  self: { id: "n1", name: "host-a", os: "linux", relay: "" },
  peers: [{ id: "n2", name: "oracle-vm", os: "linux", state: "active", connection: "direct", relay: "", sessions: 0 }],
}

const alpha = { module: "github.com/acme/alpha", commit: "abcdef1234567890", content_hash: "", cache_key: "", kind: "skill", name: "alpha", url: "", direct: true }
const beta = { module: "github.com/acme/beta", commit: "9876543210fedcba", content_hash: "", cache_key: "", kind: "skill", name: "beta", url: "", direct: true }

const project = (directory: string, packages: unknown[]) => ({
  directory,
  name: directory.split("/").pop(),
  folder_id: "",
  available: true,
  unavailable_reason: "",
  last_used: "2026-10-20T10:00:00Z",
  launches: [{ harness: "claude", mode: "default", last_used: "2026-10-20T10:00:00Z" }],
  packages,
  mcp: [],
  error: "",
})

// Each machine answers with its own projects. `node` selects the machine.
const projects: Record<string, unknown[]> = {
  local: [project("/home/oleg/agentpack", [alpha])],
  n2: [project("/srv/oracle", [alpha]), project("/srv/api", [beta])],
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

let deletes: { method: string; url: string; body: unknown }[] = []

function machineOf(url: string): string {
  return new URL(url).searchParams.get("node") ?? "local"
}

test.beforeEach(async ({ page }) => {
  deletes = []
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route(/\/api\/identity(\?.*)?$/, (route) => route.fulfill({ json: machineOf(route.request().url()) === "n2" ? peer : local }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: nodes }))
  await page.route("**/api/topology", (route) => route.fulfill({ json: topology }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/projects(\?.*)?$/, (route) => route.fulfill({ json: projects[machineOf(route.request().url())] ?? [] }))
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: sessions[machineOf(route.request().url())] ?? [] }))
  await page.route(/\/api\/recent(\?.*)?$/, (route) => {
    const request = route.request()
    if (request.method() !== "DELETE") return route.fulfill({ json: [] })
    deletes.push({ method: request.method(), url: request.url(), body: request.postDataJSON() })
    return route.fulfill({ json: { forgotten: true } })
  })
})

function matrix(page: Page) {
  return page.getByRole("table", { name: "Projects and pinned packages" })
}

test("machine detail has one matrix row per project with 7-character pins", async ({ page }) => {
  await page.goto("/machines/n2")

  const rows = matrix(page).locator("tbody tr")
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0)).toContainText("oracle")
  await expect(rows.nth(1)).toContainText("api")
  await expect(matrix(page)).toContainText("abcdef1")
  await expect(matrix(page)).not.toContainText("abcdef12")
})

test("Forget on a peer asks first, then deletes that folder on that peer", async ({ page }) => {
  await page.goto("/machines/n2")

  await matrix(page).locator("tbody tr").filter({ hasText: "api" }).click({ button: "right" })
  await page.getByRole("menuitem", { name: "Forget" }).click()

  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("Forget api?")
  expect(deletes).toHaveLength(0)

  await confirm.getByRole("button", { name: "Forget" }).click()
  await expect.poll(() => deletes.length).toBe(1)
  expect(deletes).toEqual([{ method: "DELETE", url: expect.stringContaining("/api/recent?node=n2"), body: { directory: "/srv/api" } }])
})

test("an offline machine says so in a banner", async ({ page }) => {
  await page.goto("/machines/n3")

  await expect(page.getByText("lab-pi is offline. No projects are cached.")).toBeVisible()
})

test("status bar totals projects and sessions on the machines routes only", async ({ page }) => {
  // The totals are in the status bar, hidden under 760 px, so run at desktop width.
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto("/machines/local")
  await expect(page.getByText("3 projects · 2 sessions")).toBeVisible()

  await page.goto("/sessions")
  await expect(page.getByRole("link", { name: "New session" })).toBeVisible()
  await expect(page.getByText(/ projects · /)).toHaveCount(0)
})
