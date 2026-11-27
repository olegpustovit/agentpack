import { expect, test } from "@playwright/test"
import { mockAPI, projectRow, session } from "./helpers"

const directory = session.directory
const projectURL = `/sessions/project/local/${encodeURIComponent(directory)}`
const installed = { module: "github.com/acme/docs-local", name: "Docs Local", kind: "skill", commit: "abcd1234", direct: true }
const library = { module: "github.com/acme/docs-library", name: "Docs Library", kind: "plugin", commit: "abcd1234", id: "library" }
const plugin = { kind: "plugin", name: "Docs Catalog", source: "Plugins", spec: "github.com/acme/docs-catalog" }
const server = { name: "docs-remote", url: "https://example.com/mcp", type: "http" }
const mcp = { kind: "mcp", name: "Docs Remote", source: "Servers", server }
const searchName = "Search skills, plugins and MCP servers"

test("Discover chooses a recent project and focuses the matching install result", async ({ page }) => {
  const requests = await mockAPI(page)
  const folders = Array.from({ length: 9 }, (_, i) => projectRow(`/work/project-${i}`, [], {
    last_used: `2026-11-${10 + i}T12:00:00Z`,
  }))
  await page.route("**/api/projects", (route) => route.fulfill({ json: folders }))
  await page.route((url) => url.pathname === "/api/library/catalog", (route) =>
    route.fulfill({ json: { items: [plugin], sources: [] } }),
  )
  await page.goto("/library/discover")
  await page.getByRole("button", { name: "Install Docs Catalog…" }).click()
  const picker = page.getByRole("dialog", { name: "Choose a project" })
  await expect(picker.getByRole("link").first()).toHaveText(/^project-8 · /)
  const filter = picker.getByRole("searchbox", { name: "Search projects" })
  await filter.fill("project-3")
  await expect(picker.getByRole("link")).toHaveCount(1)
  await picker.getByRole("link", { name: /^project-3 · / }).click()
  await expect(page).toHaveURL(/\/sessions\/project\/local\/%2Fwork%2Fproject-3\?install=1&find=Docs(?:%20|\+)Catalog$/i)
  const search = page.getByRole("searchbox", { name: searchName })
  await expect(search).toHaveValue("Docs Catalog")
  await expect(search).toBeFocused()
  expect(requests.filter((request) => request.method === "POST")).toHaveLength(0)
  await page.getByRole("region", { name: "Results" }).getByRole("button", { name: "Install Docs Catalog" }).click()
  await expect.poll(() => requests.find((request) => request.path === "/api/packages")).toMatchObject({
    method: "POST", body: { directory: "/work/project-3", spec: plugin.spec, restart: false },
  })
})

test("project search finds installed, Library and catalog items and installs them in place", async ({ page }) => {
  await page.clock.install()
  const requests = await mockAPI(page)
  const packages: unknown[] = [installed]
  const servers: unknown[] = []
  await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow(directory, [], { packages, mcp: servers })] }))
  await page.route("**/api/library/packages", (route) => route.fulfill({ json: [library] }))
  await page.route((url) => url.pathname === "/api/library/catalog", (route) => {
    const items = new URL(route.request().url()).searchParams.get("q") === "docs" ? [plugin, mcp] : []
    return route.fulfill({ json: { items, sources: [] } })
  })
  await page.route("**/api/packages", (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    const spec = route.request().postDataJSON().spec
    packages.push({ ...installed, module: spec.split("@")[0], name: spec === plugin.spec ? plugin.name : library.name })
    return route.fallback()
  })
  await page.route("**/api/mcp/servers", (route) => {
    servers.push({ ...server, launcher: "remote" })
    return route.fallback()
  })
  await page.goto(projectURL)
  await page.getByRole("searchbox", { name: searchName }).fill("docs")
  await page.clock.fastForward(310)
  const results = page.getByRole("region", { name: "Results" })
  await expect(results.getByText("Installed", { exact: true })).toBeVisible()
  await results.getByRole("button", { name: "Install Docs Library" }).click()
  await expect(results.getByRole("button", { name: "Remove Docs Library" })).toBeAttached()
  await results.getByRole("button", { name: "Install Docs Catalog" }).click()
  await expect(results.getByRole("button", { name: "Remove Docs Catalog" })).toBeAttached()
  await results.getByRole("button", { name: "Install Docs Remote" }).click()
  await expect(results.getByRole("switch", { name: "Use docs-remote" })).toBeChecked()
  expect(requests.filter((request) => request.method === "POST").map(({ path, body }) => [path, body])).toEqual([
    ["/api/packages", { directory, spec: `${library.module}@${library.commit}`, restart: false }],
    ["/api/packages", { directory, spec: plugin.spec, restart: false }],
    ["/api/mcp/servers", { directory, ...server, restart: false }],
  ])
})

test("a catalog outage leaves installed results and pasted GitHub installs available", async ({ page }) => {
  const requests = await mockAPI(page)
  await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow(directory, [], { packages: [installed] })] }))
  await page.route((url) => url.pathname === "/api/library/catalog", (route) => route.fulfill({ json: {
    items: [], sources: [{ name: "Plugins", count: 0, error: "Offline" }],
  } }))
  await page.goto(projectURL)
  const search = page.getByRole("searchbox", { name: searchName })
  await search.fill("docs")
  await expect(page.getByText("Couldn't reach Plugins. Offline")).toBeVisible()
  await expect(page.getByRole("region", { name: "Results" }).getByText("Docs Local")).toBeVisible()
  await search.fill("https://github.com/acme/pasted")
  await page.getByRole("button", { name: "Install acme/pasted" }).click()
  await expect(search).toHaveValue("")
  expect(requests.find(({ method, path }) => method === "POST" && path === "/api/packages")?.body)
    .toEqual({ directory, spec: "github.com/acme/pasted", restart: false })
})

test("a pasted MCP URL installs a custom server immediately", async ({ page }) => {
  const requests = await mockAPI(page)
  await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow(directory)] }))
  await page.goto(projectURL)
  const search = page.getByRole("searchbox", { name: searchName })
  const url = "https://docs.example.com/mcp"
  await search.fill(url)
  await page.getByRole("region", { name: "Results" }).getByRole("button", { name: /^Install / }).click()
  await expect(search).toHaveValue("")
  expect(requests.find(({ method, path }) => method === "POST" && path === "/api/mcp/servers")?.body)
    .toMatchObject({ directory, url, restart: false })
})

test("a project package can be removed and reinstalled from Activity", async ({ page }) => {
  const requests = await mockAPI(page)
  let present = true
  await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow(directory, [], { packages: present ? [installed] : [] })] }))
  await page.route("**/api/packages", (route) => {
    const method = route.request().method()
    if (method === "DELETE" || method === "POST") present = method === "POST"
    return route.fallback()
  })
  await page.goto(projectURL)
  const remove = page.getByRole("button", { name: "Remove Docs Local" })
  await remove.click()
  await expect(remove).toHaveCount(0)
  await page.getByRole("button", { name: /Removed Docs Local.*Show activity/ }).click()
  await page.getByRole("listitem").filter({ hasText: "Removed Docs Local" }).getByRole("button", { name: "Undo" }).click()
  await expect(remove).toBeVisible()
  expect(requests.filter(({ method, path }) => method !== "GET" && path === "/api/packages")
    .map(({ method, body }) => [method, body])).toEqual([
    ["DELETE", { directory, module: installed.module }],
    ["POST", { directory, spec: `${installed.module}@${installed.commit}`, restart: true }],
  ])
})

test("an older peer shows an update message when package removal is unavailable", async ({ page }) => {
  await mockAPI(page, { fleet: true })
  await page.route((url) => url.pathname === "/api/projects", (route) =>
    route.fulfill({ json: [projectRow(directory, [], { packages: [installed] })] }))
  const statuses = [404, 405]
  await page.route((url) => url.pathname === "/api/packages" && url.searchParams.get("node") === "peer-1", (route) => {
    if (route.request().method() !== "DELETE") return route.fallback()
    const status = statuses.shift()
    return route.fulfill({ status, body: status === 404 ? "404 page not found" : "Method Not Allowed" })
  })
  await page.goto(`/sessions/project/peer-1/${encodeURIComponent(directory)}`)
  const remove = page.getByRole("button", { name: "Remove Docs Local" })
  for (const status of [404, 405]) {
    const response = page.waitForResponse((res) => res.request().method() === "DELETE" && res.status() === status)
    await remove.click()
    await response
    await expect(page.getByText("Update agentpack on Build server to remove packages")).toBeVisible()
  }
})
