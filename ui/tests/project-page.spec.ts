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

test("a pasted MCP URL can be named before install and renamed in Edit", async ({ page }) => {
  const requests = await mockAPI(page, { fleet: true })
  let projectServer: { name: string; url: string; type: string } | undefined
  await page.route((url) => url.pathname === "/api/projects", (route) => route.fulfill({
    json: [projectRow(directory, [], { mcp: projectServer ? [{ ...projectServer, transport: projectServer.type, launcher: "remote", source: "manifest" }] : [] })],
  }))
  await page.route((url) => url.pathname === "/api/mcp/servers", (route) => {
    projectServer = route.request().postDataJSON()
    return route.fallback()
  })
  let outdated = true
  const edits: unknown[] = []
  await page.route((url) => url.pathname === "/api/projects/settings", (route) => {
    const body = route.request().postDataJSON()
    edits.push(body)
    if (outdated) return route.fulfill({ status: 400, body: "invalid request" })
    projectServer = body.servers[0]
    return route.fulfill({ status: 204 })
  })

  await page.goto(projectURL.replace("/local/", "/peer-1/"))
  const search = page.getByRole("searchbox", { name: searchName })
  const url = "https://docs.example.com/mcp"
  await search.fill(url)
  const results = page.getByRole("region", { name: "Results" })
  const name = results.getByRole("textbox", { name: "Server name" })
  await expect(name).not.toHaveValue("")
  await name.fill("team-docs")
  await results.getByRole("button", { name: "Install team-docs" }).click()
  await expect(search).toHaveValue("")
  expect(requests.find(({ method, path }) => method === "POST" && path === "/api/mcp/servers")?.body)
    .toEqual({ directory, name: "team-docs", url, type: "http", restart: false })

  await page.getByRole("button", { name: "Edit team-docs" }).click()
  const edit = page.getByRole("form", { name: "Edit team-docs" })
  const nextURL = "https://docs.example.com/v2/mcp"
  await edit.getByRole("textbox", { name: "Name", exact: true }).fill("handbook")
  await edit.getByRole("textbox", { name: "URL" }).fill(nextURL)
  await edit.getByRole("button", { name: "Save" }).click()
  await expect(edit.getByText("Update agentpack on Build server to rename servers")).toBeVisible()
  outdated = false
  await edit.getByRole("button", { name: "Save" }).click()
  await expect(page.getByRole("button", { name: "Edit handbook" })).toBeAttached()
  expect(edits.at(-1)).toMatchObject({
    directory, rename_mcp: { "team-docs": "handbook" }, servers: [{ name: "handbook", url: nextURL, type: "http" }],
  })
})
