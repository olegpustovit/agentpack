import { expect, test, type Page } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
const nodes = [{ id: "n2", name: "oracle-vm", online: true }]

const skill = (module: string, name: string) => ({
  id: `${module}-record`,
  module,
  commit: "abcd1234ef56",
  kind: "skill",
  url: `https://${module}`,
  owner: "acme",
  repo: name,
  cache_key: `${name}-key`,
  name,
})

const mcp = (name: string) => ({
  name,
  source: "",
  launcher: "npm",
  status: "ok",
  package: `@acme/${name}`,
  version: "1.2.3",
  integrity: "",
  host: "",
  reason: "",
})

const folder = (directory: string, name: string, mcpServers: unknown[]) => ({
  directory,
  name,
  folder_id: "",
  available: true,
  unavailable_reason: "",
  last_used: "2026-10-20T10:00:00Z",
  launches: [],
  packages: [],
  mcp: mcpServers,
  error: "",
})

// Each machine answers the three library endpoints with its own data. `node` selects the machine.
const machines: Record<string, { packages: unknown[]; agentpacks: unknown[]; projects: unknown[] }> = {
  local: {
    packages: [skill("github.com/acme/alpha", "alpha-skill")],
    agentpacks: [],
    projects: [folder("/home/oleg/agentpack", "agentpack", [mcp("foo-tools"), mcp("bar")])],
  },
  n2: {
    packages: [],
    agentpacks: [
      {
        agentpack: { id: "pk1", name: "team-pack", revisions: ["rev1"], heads: ["rev1"] },
        revisions: [{ id: "rev1", agentpack_id: "pk1", manifest: "", lock: "", created_at: "2026-10-01T00:00:00Z", project: "oracle" }],
      },
    ],
    projects: [folder("/srv/oracle", "oracle", [mcp("foo-tools")])],
  },
}

function machineOf(url: string): string {
  return new URL(url).searchParams.get("node") ?? "local"
}

async function mockLibrary(page: Page) {
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: nodes }))
  // Aborting the stream keeps the queries on their plain fetch path.
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/library\/packages(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.packages }))
  await page.route(/\/api\/library\/agentpacks(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.agentpacks }))
  await page.route(/\/api\/projects(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.projects }))
}

test.beforeEach(async ({ page }) => {
  await mockLibrary(page)
})

test("type and search survive reload and filter the list", async ({ page }) => {
  await page.goto("/library?type=mcp&q=foo")
  const list = page.getByRole("navigation", { name: "Library" })
  await expect(list.getByRole("link")).toHaveCount(1)
  await expect(list.getByRole("link", { name: /foo-tools/ })).toBeVisible()
  await expect(page.getByRole("searchbox", { name: "Search library" })).toHaveValue("foo")

  await page.reload()
  await expect(page).toHaveURL(/\/library\?type=mcp&q=foo$/)
  await expect(list.getByRole("link")).toHaveCount(1)
  await expect(list.getByRole("link", { name: /foo-tools/ })).toBeVisible()
})

test("an MCP server merged across machines shows its pin and project count", async ({ page }) => {
  await page.goto("/library?type=mcp")
  const list = page.getByRole("navigation", { name: "Library" })
  await expect(list.getByRole("link", { name: /foo-tools/ })).toContainText("npm · pinned 1.2.3 · 2 projects")
  await expect(list.getByRole("link", { name: /bar/ })).toContainText("npm · pinned 1.2.3 · 1 project")
})

test("All groups rows under type headers with counts", async ({ page }) => {
  await page.goto("/library")
  const list = page.getByRole("navigation", { name: "Library" })
  await expect(list.locator('[data-slot="group-header"]')).toHaveText(["Skills1", "MCP servers2", "Agentpacks1"])
})

test("typing in search writes q to the URL", async ({ page }) => {
  await page.goto("/library")
  const list = page.getByRole("navigation", { name: "Library" })
  await page.getByRole("searchbox", { name: "Search library" }).pressSequentially("bar")
  await expect(page).toHaveURL(/\/library\?q=bar$/)
  await expect(list.getByRole("link")).toHaveCount(1)
  await expect(list.getByRole("link", { name: /bar/ })).toBeVisible()
})

test("selecting a row opens its detail and keeps the query", async ({ page }) => {
  await page.goto("/library?type=mcp&q=foo")
  await page.getByRole("navigation", { name: "Library" }).getByRole("link", { name: /foo-tools/ }).click()
  await expect(page).toHaveURL(/\/library\/mcp\/foo-tools\?type=mcp&q=foo$/)
})

test("sort popup writes sort to the URL", async ({ page }) => {
  await page.goto("/library")
  await page.getByRole("combobox", { name: "Sort" }).click()
  await page.getByRole("option", { name: "Recently updated" }).click()
  await expect(page).toHaveURL(/\/library\?sort=updated$/)
})

test("status bar shows library totals on /library and not on /sessions", async ({ page }) => {
  await page.goto("/library")
  await expect(page.getByRole("contentinfo", { includeHidden: true })).toContainText("4 items · 1 skill · 0 plugins · 2 MCP")
  await page.goto("/sessions")
  await expect(page.getByRole("contentinfo", { includeHidden: true })).not.toContainText("items ·")
})
