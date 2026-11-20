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
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
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
  await expect(list.getByRole("link").filter({ hasNotText: "Discover" })).toHaveCount(1)
  await expect(list.getByRole("link", { name: /foo-tools/ })).toBeVisible()
  await expect(page.getByRole("searchbox", { name: "Search library" })).toHaveValue("foo")

  await page.reload()
  await expect(page).toHaveURL(/\/library\?type=mcp&q=foo$/)
  await expect(list.getByRole("link").filter({ hasNotText: "Discover" })).toHaveCount(1)
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
  await expect(list.getByRole("link").filter({ hasNotText: "Discover" })).toHaveCount(1)
  await expect(list.getByRole("link", { name: /bar/ })).toBeVisible()
})

test("selecting a row opens its detail and keeps the query", async ({ page }) => {
  await page.goto("/library?type=mcp&q=foo")
  await page.getByRole("navigation", { name: "Library" }).getByRole("link", { name: /foo-tools/ }).click()
  await expect(page).toHaveURL(/\/library\/mcp\/foo-tools\?type=mcp&q=foo$/)
})

test("Discover lists the catalog and Install… opens Add package with the spec filled in", async ({ page }) => {
  await page.route(/\/api\/library\/catalog(\?.*)?$/, (route) =>
    route.fulfill({
      json: {
        items: [
          { kind: "plugin", name: "hookify", description: "Turns corrections into hooks.", publisher: "Anthropic", source: "claude-plugins-official", spec: "github.com/anthropics/claude-plugins-official/plugins/hookify" },
          { kind: "mcp", name: "foo-tools", source: "MCP Registry", command: "agentpack mcp add foo-tools --command npx --args -y @acme/foo-tools" },
        ],
        sources: [{ name: "claude-plugins-official", count: 1 }, { name: "MCP Registry", count: 1, error: "502 Bad Gateway" }],
      },
    }),
  )
  await page.goto("/library")
  await page.getByRole("navigation", { name: "Library" }).getByRole("link", { name: "Discover" }).click()
  await expect(page).toHaveURL(/\/library\/discover$/)
  const catalog = page.getByRole("list", { name: "Catalog" })
  await expect(catalog.getByRole("listitem").filter({ hasText: "foo-tools" })).toContainText("In 2 projects")
  await expect(page.getByText("Couldn't reach MCP Registry: 502 Bad Gateway")).toBeVisible()
  await catalog.getByRole("button", { name: "Install hookify…" }).click()
  await expect(page.getByRole("dialog").getByLabel("Package", { exact: true })).toHaveValue("github.com/anthropics/claude-plugins-official/plugins/hookify")
})

test("Discover installs an MCP server into a chosen project; an older peer reads Needs update", async ({ page }) => {
  const server = { name: "new-server", command: "npx", args: ["-y", "@acme/new-server"] }
  await page.route(/\/api\/library\/catalog(\?.*)?$/, (route) =>
    route.fulfill({ json: { items: [{ kind: "mcp", name: "new-server", source: "MCP Registry", command: "agentpack mcp add new-server --command npx --args -y @acme/new-server", server }], sources: [] } }),
  )
  const posted: { node: string; body: unknown }[] = []
  await page.route(/\/api\/mcp\/servers(\?.*)?$/, (route) => {
    const node = machineOf(route.request().url())
    posted.push({ node, body: route.request().postDataJSON() })
    return node === "n2" ? route.fulfill({ status: 404, body: "404 page not found" }) : route.fulfill({ json: { installed: true } })
  })
  await page.goto("/library/discover")
  const row = page.getByRole("list", { name: "Catalog" }).getByRole("listitem").filter({ hasText: "new-server" })
  await expect(row.getByRole("button", { name: "Copy the command that adds new-server" })).toBeVisible()
  await row.getByRole("button", { name: "Install new-server…" }).click()
  const dialog = page.getByRole("dialog")
  await dialog.getByRole("combobox", { name: "Project" }).click()
  await page.getByRole("option", { name: /oracle/ }).click()
  await dialog.getByRole("button", { name: "Install", exact: true }).click()
  await expect(dialog).toContainText("Couldn't install new-server: Needs update")
  await dialog.getByRole("combobox", { name: "Project" }).click()
  await page.getByRole("option", { name: /agentpack/ }).click()
  await dialog.getByRole("button", { name: "Install", exact: true }).click()
  await expect(dialog).toBeHidden()
  expect(posted.at(-1)).toEqual({ node: "local", body: { ...server, directory: "/home/oleg/agentpack", restart: true } })
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

test("remote MCP servers show their shared login and connect in place", async ({ page }) => {
  const remote = (name: string, url: string) => ({ name, source: "plugin", launcher: "remote", status: "unpinnable", package: "", version: "", integrity: "", host: new URL(url).host, reason: "", url })
  await page.route(/\/api\/projects(\?.*)?$/, (route) => route.fulfill({
    json: machineOf(route.request().url()) === "local" ? [folder("/w/admin", "admin", [remote("cloudflare", "https://mcp.cloudflare.com/mcp"), remote("linear", "https://mcp.linear.app/mcp")])] : [],
  }))
  await page.route("**/api/mcp/credentials", (route) => route.fulfill({
    json: { callback_url: "", credentials: [{ id: "c1", match: "https://mcp.linear.app/mcp", kind: "oauth", label: "linear", status: "connected" }] },
  }))
  let login: Record<string, unknown> | undefined
  await page.route("**/api/mcp/logins", (route) => {
    login = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { login: "l1", authorize_url: "/library?type=mcp" } })
  })
  await page.goto("/library/mcp/cloudflare?type=mcp")
  const list = page.getByRole("navigation", { name: "Library" })
  await expect(list.getByRole("link", { name: /linear/ })).toContainText("Connected")
  await expect(list.getByRole("link", { name: /cloudflare/ })).toContainText("Not connected")
  await page.getByRole("button", { name: "Connect…" }).click()
  await expect.poll(() => login).toEqual({ url: "https://mcp.cloudflare.com/mcp", label: "cloudflare", return_to: "/library/mcp/cloudflare?type=mcp" })
})
