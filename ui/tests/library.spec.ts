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
  repo: module.split("/")[2],
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

test("Discover lists installable catalog items and source errors", async ({ page }) => {
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
  await expect(catalog.getByRole("button", { name: "Install hookify…" })).toBeVisible()
})

test("Discover hands an MCP definition to the project page and reports an older peer", async ({ page }) => {
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
  await row.getByRole("button", { name: "More actions for new-server" }).click()
  await expect(page.getByRole("menuitem", { name: "Copy command" })).toBeVisible()
  await page.keyboard.press("Escape")
  await row.getByRole("button", { name: "Install new-server…" }).click()
  await page.getByRole("dialog", { name: "Choose a project" }).getByRole("link", { name: "oracle · oracle-vm" }).click()
  await page.getByRole("button", { name: "Install new-server" }).click()
  await expect(page.getByText("Couldn't install new-server. Update agentpack on oracle-vm to change this project.")).toBeVisible()
  expect(posted).toEqual([{ node: "n2", body: { ...server, directory: "/srv/oracle", restart: false } }])
})

test("the MCP inspector can install a pinned npm server absent from the catalog", async ({ page }) => {
  await page.route("**/api/library/catalog**", (route) => route.fulfill({ json: { items: [], sources: [] } }))
  await page.route("**/api/projects*", (route) => {
    const rows = machineOf(route.request().url()) === "local"
      ? [...machines.local!.projects, folder("/home/oleg/other", "other", [])]
      : machines.n2!.projects
    return route.fulfill({ json: rows })
  })
  await page.route("**/api/mcp/servers", (route) => route.fulfill({ json: { installed: true } }))
  await page.goto("/library/mcp/foo-tools")
  await page.getByRole("button", { name: "Install in another project…" }).click()
  await page.getByRole("dialog", { name: "Choose a project" }).getByRole("link", { name: "other · host-a" }).click()
  const posted = page.waitForRequest((request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/mcp/servers")
  await page.getByRole("button", { name: "Install foo-tools" }).click()
  expect((await posted).postDataJSON()).toEqual({
    directory: "/home/oleg/other", name: "foo-tools", command: "npx", args: ["-y", "@acme/foo-tools@1.2.3"], restart: false,
  })
})

test("sort popup writes sort to the URL", async ({ page }) => {
  await page.goto("/library")
  await page.getByRole("combobox", { name: "Sort" }).click()
  await page.getByRole("option", { name: "Recently updated" }).click()
  await expect(page).toHaveURL(/\/library\?sort=updated$/)
})

test("the Library toolbar shows item and machine totals", async ({ page }) => {
  await page.goto("/library")
  await expect(page.locator('[data-slot="toolbar"]')).toContainText("4 items on 2 machines")
})

test("remote MCP servers show their shared login and connect in place", async ({ page }) => {
  const remote = (name: string, url: string) => ({ name, source: "plugin", launcher: "remote", status: "unpinnable", package: "", version: "", integrity: "", host: new URL(url).host, reason: "", url })
  await page.route(/\/api\/projects(\?.*)?$/, (route) => route.fulfill({
    json: machineOf(route.request().url()) === "local" ? [folder("/w/admin", "admin", [remote("cloudflare", "https://mcp.cloudflare.com/mcp"), remote("linear", "https://mcp.linear.app/mcp")])] : [],
  }))
  await page.route("**/api/mcp/credentials", (route) => route.fulfill({
    json: { callback_url: "", credentials: [
      { id: "c1", match: "https://mcp.linear.app/mcp", kind: "oauth", label: "Work", status: "connected" },
      { id: "c2", match: "https://mcp.linear.app/mcp", kind: "oauth", label: "Client", status: "connected" },
    ] },
  }))
  let login: Record<string, unknown> | undefined
  await page.route("**/api/mcp/logins", (route) => {
    login = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { login: "l1", authorize_url: "/library?type=mcp" } })
  })
  await page.goto("/library/mcp/linear?type=mcp")
  await expect(page.getByText("Work · default of 2 logins")).toBeVisible()
  await expect(page.getByRole("link", { name: "Manage logins…" })).toBeVisible()
  await page.goto("/library/mcp/cloudflare?type=mcp")
  const list = page.getByRole("navigation", { name: "Library" })
  await expect(list.getByRole("link", { name: /linear/ })).toContainText("Connected")
  await expect(list.getByRole("link", { name: /cloudflare/ })).toContainText("Not connected")
  await page.getByRole("button", { name: "Sign in…" }).click()
  await expect.poll(() => login).toEqual({ url: "https://mcp.cloudflare.com/mcp", label: "cloudflare", return_to: "/library/mcp/cloudflare?type=mcp" })
})

test("LIB-01: partial update check never says Up to date, Retry failed refetches only failed sources, and empty filter offers Clear filters", async ({ page }) => {
  await page.route(/\/api\/projects(\?.*)?$/, (route) =>
    route.fulfill({
      json:
        machineOf(route.request().url()) === "local"
          ? [
              {
                ...folder("/home/oleg/agentpack", "agentpack", []),
                packages: [
                  { module: "github.com/acme/alpha", commit: "abcd1234ef56", content_hash: "", cache_key: "", kind: "skill", name: "alpha-skill", url: "", direct: true },
                  { module: "github.com/acme/beta", commit: "111122223333", content_hash: "", cache_key: "", kind: "skill", name: "beta-skill", url: "", direct: true },
                ],
              },
            ]
          : [],
    }),
  )
  const latestCalls: string[] = []
  let failBeta = true
  await page.route("**/api/library/latest**", (route) => {
    const body = route.request().postDataJSON() as { repo: string }
    latestCalls.push(body.repo)
    if (body.repo === "beta" && failBeta) return route.fulfill({ status: 500, body: "rate limited" })
    return route.fulfill({ json: { commit: body.repo === "alpha" ? "abcd1234ef56" : "111122223333", current: [body.repo === "alpha" ? "abcd1234ef56" : "111122223333"], exact: true } })
  })

  await page.goto("/library")
  await page.getByRole("button", { name: "Check for updates" }).click()
  const statusButton = page.getByRole("button", { name: /Checked 2 · 1 failed · Retry failed/ })
  await expect(statusButton).toBeVisible()
  await expect(statusButton).not.toContainText("Up to date")

  failBeta = false
  latestCalls.length = 0
  await statusButton.click()
  await expect(page.getByRole("button", { name: "Up to date" })).toBeVisible()
  expect(latestCalls).toEqual(["beta"])

  await page.getByRole("searchbox", { name: "Search library" }).fill("nonexistent-xyz")
  await expect(page.getByText("No matches")).toBeVisible()
  await page.getByRole("button", { name: "Clear filters" }).click()
  await expect(page.getByRole("searchbox", { name: "Search library" })).toHaveValue("")
  await expect(page.getByRole("navigation", { name: "Library" }).getByRole("link", { name: /alpha-skill/ })).toBeVisible()
})

test("LIB-02, LIB-03 and AG-01: Discover has independent catalog search, installed rows link and install, and the picker preserves catalog search", async ({ page }) => {
  await page.route(/\/api\/projects(\?.*)?$/, (route) =>
    route.fulfill({
      json:
        machineOf(route.request().url()) === "local"
          ? [
              {
                ...folder("/home/oleg/agentpack", "agentpack", [mcp("foo-tools")]),
                packages: [{ module: "github.com/acme/alpha", commit: "abcd1234ef56", content_hash: "", cache_key: "alpha-key", kind: "skill", name: "alpha-skill", url: "", direct: true }],
              },
              folder("/home/oleg/other", "other", []),
            ]
          : [folder("/srv/oracle", "oracle", [mcp("foo-tools")])],
    }),
  )
  const fooServer = { name: "foo-tools", command: "npx", args: ["-y", "@acme/foo-tools"] }
  await page.route(/\/api\/library\/catalog(\?.*)?$/, (route) => {
    const q = new URL(route.request().url()).searchParams.get("q")
    return route.fulfill({
      json: {
        items: [
          { kind: "plugin", name: "Redwood tools", description: "Long description that wraps cleanly.", publisher: "Acme", source: "marketplace", spec: "github.com/acme/redwood" },
          { kind: "plugin", name: "alpha-skill", description: "Installed plugin.", publisher: "Acme", source: "marketplace", spec: "github.com/acme/alpha" },
          ...(!q ? [{ kind: "mcp", name: "foo-tools", source: "MCP Registry", server: fooServer, command: "agentpack mcp add foo-tools" }] : []),
        ],
        sources: [{ name: "marketplace", count: 2 }],
      },
    })
  })

  await page.goto("/library/discover?q=red")
  const search = page.getByRole("searchbox", { name: "Search catalog" })
  await expect(search).toBeVisible()
  await search.fill("Redwood")
  await expect(page).toHaveURL(/\/library\/discover\?q=red$/)
  await expect(page.getByRole("list", { name: "Catalog" }).getByRole("listitem")).toHaveCount(1)

  // AG-01: Closing the picker keeps ?q=red and catalog search.
  await page.getByRole("button", { name: "Install Redwood tools…" }).click()
  await expect(page.getByRole("dialog")).toHaveCount(1)
  await expect(page.getByRole("dialog", { name: "Choose a project" }).getByRole("link", { name: "agentpack · host-a" })).toBeVisible()
  await page.keyboard.press("Escape")
  await expect(page.getByRole("dialog")).toHaveCount(0)
  await expect(page).toHaveURL(/\/library\/discover\?q=red$/)
  await expect(search).toHaveValue("Redwood")

  await page.getByRole("button", { name: "Clear", exact: true }).click()
  await expect(search).toHaveValue("")
  await expect(page).toHaveURL(/\/library\/discover\?q=red$/)

  // LIB-03: Installed plugin row shows both link ("In 1 project") and "Install in another project", opening the project picker.
  const alphaRow = page.getByRole("list", { name: "Catalog" }).getByRole("listitem").filter({ hasText: "alpha-skill" })
  await expect(alphaRow.getByRole("link", { name: "In 1 project" })).toBeVisible()
  await alphaRow.getByRole("button", { name: /Install alpha-skill in another project/ }).click()
  await expect(page.getByRole("dialog", { name: "Choose a project" }).getByRole("link", { name: "other · host-a" })).toBeVisible()
  await page.keyboard.press("Escape")

  // Installed MCP row shows both link ("In 2 projects") and "Install in another project".
  const fooRow = page.getByRole("list", { name: "Catalog" }).getByRole("listitem").filter({ hasText: "foo-tools" })
  await expect(fooRow.getByRole("link", { name: "In 2 projects" })).toBeVisible()
  await expect(fooRow.getByRole("button", { name: /Install foo-tools in another project/ })).toBeVisible()
})

test.describe("phone viewport", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })

  test("LIB-02 and AG-01 on phone: Discover search is visible and editable, and closing the picker keeps ?q=red", async ({ page }) => {
    await page.route(/\/api\/library\/catalog(\?.*)?$/, (route) =>
      route.fulfill({
        json: {
          items: [{ kind: "plugin", name: "Redwood tools", description: "Wraps cleanly on phone.", publisher: "Acme", source: "marketplace", spec: "github.com/acme/redwood" }],
          sources: [{ name: "marketplace", count: 1 }],
        },
      }),
    )
    await page.goto("/library/discover?q=red")
    const search = page.getByRole("searchbox", { name: "Search catalog" })
    await expect(search).toBeVisible()
    const box = await search.boundingBox()
    expect(box?.height ?? 0).toBeGreaterThanOrEqual(44)
    await search.fill("Redwood")
    await page.getByRole("button", { name: "Install Redwood tools…" }).click()
    await expect(page.getByRole("dialog")).toHaveCount(1)
    await expect(page.getByRole("dialog", { name: "Choose a project" }).getByRole("link", { name: "agentpack · host-a" })).toBeVisible()
    await page.keyboard.press("Escape")
    await expect(page.getByRole("dialog")).toHaveCount(0)
    await expect(page).toHaveURL(/\/library\/discover\?q=red$/)
    await page.getByRole("button", { name: "Clear", exact: true }).click()
    await expect(search).toHaveValue("")
    await expect(page).toHaveURL(/\/library\/discover\?q=red$/)
  })
})
