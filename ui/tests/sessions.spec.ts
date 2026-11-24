import { expect, test } from "@playwright/test"
import { conversationImage, mockAPI, projectRow } from "./helpers"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
const terminal = (running: boolean) => ({ interactive: true, running, restarting: false, rows: 30, columns: 100, exit_code: 0 })

const sessions = [
  {
    id: "s1",
    name: "build",
    directory: "/home/oleg/agentpack",
    harness: "claude",
    mode: "default",
    state: terminal(true),
    started_at: twoHoursAgo,
    last_used: twoHoursAgo,
    started_by: { kind: "browser", user: "oleg@" },
  },
  {
    id: "s2",
    directory: "/home/oleg/oracle",
    harness: "codex",
    mode: "plan",
    state: terminal(false),
    started_at: twoHoursAgo,
    last_used: twoHoursAgo,
  },
]
const renamed = [sessions[0], { ...sessions[1], name: "oracle main" }]

// What GET /api/sessions returns. Tests change it to simulate new data on the next poll.
let reply: unknown[] = sessions
let fetches = 0

test.beforeEach(async ({ page }) => {
  await mockAPI(page)
  reply = sessions
  fetches = 0
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
  // Aborting the stream makes the list poll on its 15 s fallback, as it does without live events.
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    fetches += 1
    return route.fulfill({ json: reply })
  })
})

test("focus on a row survives a refetch that changes the data", async ({ page }) => {
  await page.clock.install()
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows).toHaveCount(2)
  await rows.first().focus()

  reply = renamed
  const polls = fetches
  await page.clock.runFor(16_000)
  await expect.poll(() => fetches).toBeGreaterThan(polls)
  await expect(page.getByRole("link", { name: /oracle main/ })).toBeVisible()
  await expect(rows.first()).toBeFocused()
})

test("arrow keys open each session at once and leave one history entry", async ({ page }) => {
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await rows.first().focus()

  const before = await page.evaluate(() => history.length)
  await page.keyboard.press("ArrowDown")
  await page.keyboard.press("ArrowUp")
  await page.keyboard.press("ArrowDown")
  await expect(page).toHaveURL(/\/sessions\/local\/s2$/)
  expect(await page.evaluate(() => history.length)).toBe(before + 1)
})

test("New session claims a Cursor CLI chat together with its first message", async ({ page }) => {
  const project = {
    directory: "/home/oleg/agentpack",
    name: "agentpack",
    folder_id: "f1",
    available: true,
    unavailable_reason: "",
    last_used: twoHoursAgo,
    launches: [],
    packages: [],
    mcp: [],
    error: "",
  }
  await page.route("**/api/projects", (route) => route.fulfill({ json: [project] }))
  const starting = { ...sessions[0], id: "s9", harness: "agent", transport: "acp", conversation_state: "starting", state: terminal(false) }
  // Only the POST is answered here; GET falls through to the shared sessions mock.
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    reply = [...sessions, starting]
    return route.fulfill({ json: starting })
  })
  await page.route("**/api/sessions/s9/conversation", (route) => route.fulfill({
    body: '{"seq":1,"ts":1,"kind":"queued_prompt","payload":{"queueId":"new-chat","text":{"text":"tidy the overlay"}}}\n',
    contentType: "application/x-ndjson",
  }))

  await page.goto("/sessions?new=1")
  await page.getByRole("group", { name: "Agent" }).getByRole("button", { name: "Cursor CLI" }).click()
  await page.getByRole("textbox", { name: "First message" }).fill("tidy the overlay")
  const posted = page.waitForRequest((request) => request.method() === "POST" && /\/api\/sessions$/.test(request.url()))
  await page.getByRole("button", { name: "Start session" }).click()
  const body = (await posted).postDataJSON()
  expect(body).toMatchObject({ directory: "/home/oleg/agentpack", harness: "agent", first_message: "tidy the overlay" })
  await expect(page.getByRole("list", { name: "Queued messages" })).toContainText("tidy the overlay")
})

test("New session browses and starts in an unlisted folder using the keyboard", async ({ page }) => {
  await page.route(/\/api\/sessions(\?.*)?$/, (route) =>
    route.request().method() === "POST" ? route.fulfill({ json: { ...sessions[0], id: "created", directory: "/home/viewer/projects" } }) : route.fallback(),
  )
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "old", name: "Old gateway", online: true }] }))
  await page.route((url) => url.pathname.startsWith("/api/folders") && url.searchParams.get("node") === "old",
    (route) => route.fulfill({ status: 404, body: "404 page not found" }),
  )
  await page.goto("/sessions?new=1&machine=old")
  const field = page.getByPlaceholder("Search projects, or type a path")
  await field.fill("~/")
  await expect(page.getByRole("alert")).toHaveText("Update Agentpack on Old gateway to browse its folders")
  await field.press("Escape")
  await expect(page.getByRole("navigation", { name: "Projects" })).toBeVisible()
  const machine = page.getByRole("combobox", { name: "Filter by machine", exact: true })
  await machine.click()
  const allMachines = page.getByRole("option", { name: /^All machines/ })
  await expect(allMachines).toHaveText(/All machines\s*1$/)
  await allMachines.click()
  await field.fill("~/pro")
  await expect(machine.locator('[data-slot="select-value"]')).toHaveText("host-a")
  await machine.click()
  await expect(allMachines).toHaveCount(0)
  const olderPeer = page.getByRole("option", { name: /^Old gateway/ })
  await expect(olderPeer).toBeDisabled()
  await expect(olderPeer).toContainText("needs update")
  await page.getByRole("option", { name: /^host-a/ }).click()
  await expect(page.getByRole("navigation", { name: "Folders" }).getByRole("button", { name: "projects" })).toBeVisible()
  await field.press("Tab")
  await expect(field).toHaveValue("~/projects/")
  await expect(page.getByRole("button", { name: /Use this folder/ })).toBeVisible()
  await field.press("Enter")
  await expect(page.getByRole("heading", { name: "projects", exact: true })).toBeVisible()
  const posted = page.waitForRequest((request) => request.method() === "POST" && /\/api\/sessions$/.test(request.url()))
  await field.press("Control+Enter")
  expect((await posted).postDataJSON()).toMatchObject({ directory: "/home/viewer/projects" })
})

test("New session creates and selects a folder on the chosen machine", async ({ page }) => {
  const parent = "/Users/viewer/projects"
  const path = parent + "/new-project"
  const onMachine = (url: URL) => url.searchParams.get("node") === "mac-air"
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "mac-air", name: "MacBook Air", online: true }] }))
  await page.route((url) => url.pathname === "/api/folders" && onMachine(url), (route) => {
    if (route.request().method() === "POST") return route.fulfill({ json: { path } })
    const directory = new URL(route.request().url()).searchParams.get("path") === "~/projects/" ? parent : path
    return route.fulfill({ json: {
      path: directory, parent, row: projectRow(directory, [], { last_used: "" }), entries: [], truncated: false,
    } })
  })
  await page.route((url) => url.pathname === "/api/sessions" && onMachine(url), (route) =>
    route.request().method() === "POST" ? route.fulfill({ json: { ...sessions[0], id: "created", directory: path } }) : route.fallback(),
  )
  await page.goto("/sessions?new=1&machine=mac-air")
  const field = page.getByPlaceholder("Search projects, or type a path")
  await field.fill("~/projects/new-project")
  const created = page.waitForRequest((req) => req.method() === "POST" && new URL(req.url()).pathname === "/api/folders")
  await page.getByRole("button", { name: "Create folder “new-project”" }).click()
  const request = await created
  expect(onMachine(new URL(request.url()))).toBe(true)
  expect(request.postDataJSON()).toEqual({ parent, name: "new-project" })
  await expect(field).toHaveValue(path + "/")
  await expect(page.getByRole("heading", { name: "new-project", exact: true })).toBeVisible()
  const started = page.waitForRequest((req) => req.method() === "POST" && new URL(req.url()).pathname === "/api/sessions")
  await page.getByRole("button", { name: "Start session" }).click()
  const launch = await started
  expect(onMachine(new URL(launch.url()))).toBe(true)
  expect(launch.postDataJSON()).toMatchObject({ directory: path })
})

test("New session shows what the chosen mode loads", async ({ page }) => {
  const project = {
    directory: "/home/oleg/agentpack",
    name: "agentpack",
    available: true,
    last_used: twoHoursAgo,
    launches: [{ harness: "claude", mode: "default", last_used: twoHoursAgo }],
    packages: [{ module: "github.com/acme/heavy", name: "heavy", kind: "plugin", direct: true }],
    mcp: [{ name: "playwright", launcher: "npm", status: "pinned", package: "@playwright/mcp", version: "1.0.0", source: "plugin" }],
    modes: [
      { name: "default", off_packages: [], partial_packages: [], off_mcp: [] },
      { name: "design", off_packages: ["github.com/acme/heavy"], partial_packages: [], off_mcp: ["playwright"] },
    ],
    error: "",
  }
  await page.route("**/api/projects", (route) => route.fulfill({ json: [project] }))
  await page.goto("/sessions?new=1")
  const loaded = page.getByRole("region", { name: "Loaded in this session" })
  await expect(loaded).toContainText("2 active")
  await expect(loaded).toContainText("@playwright/mcp@1.0.0")
  await page.getByRole("group", { name: "Mode" }).getByRole("button", { name: "design" }).click()
  await expect(loaded).toContainText("0 active, 2 turned off by mode “design”")
  await expect(loaded.getByRole("listitem").filter({ hasText: "heavy" })).toContainText("off in design")
})

test("New session loads protected project details after the project is selected", async ({ page }) => {
  const directory = "/Users/viewer/Documents/private-project"
  const summary = projectRow(directory, [{ harness: "claude" }], { details_deferred: true })
  const details = {
    ...summary,
    details_deferred: false,
    modes: [{ name: "review", off_packages: [], partial_packages: [], off_mcp: [] }],
  }
  const reads: string[] = []
  await page.route((url) => url.pathname === "/api/projects", (route) => {
    const selected = new URL(route.request().url()).searchParams.get("directory")
    if (selected) reads.push(selected)
    return route.fulfill({ json: [selected ? details : summary] })
  })
  await page.goto("/sessions?new=1")
  await expect(page.getByText("Select this project to see what it loads.")).toBeVisible()
  await expect(page.getByRole("button", { name: "Start session" })).toBeEnabled()
  expect(reads).toEqual([])
  await page.getByRole("button", { name: /private-project/ }).click()
  await expect(page.getByRole("group", { name: "Mode" }).getByRole("button", { name: "review" })).toBeVisible()
  expect(reads).toEqual([directory])
})

test("New session saves the project's pick among several logins", async ({ page }) => {
  const linear = "https://mcp.linear.app/mcp"
  let login = ""
  const project = () => ({
    directory: "/home/oleg/agentpack", name: "agentpack", available: true, last_used: twoHoursAgo,
    launches: [{ harness: "claude", mode: "default", last_used: twoHoursAgo }], packages: [], error: "",
    mcp: [{ name: "linear", launcher: "remote", status: "unpinnable", package: "", version: "", source: "manifest", url: linear, ...(login && { login }) }],
  })
  await page.route("**/api/projects", (route) => route.fulfill({ json: [project()] }))
  await page.route("**/api/mcp/credentials", (route) => route.fulfill({ json: { callback_url: "", credentials: [
    { id: "c1", match: linear, kind: "oauth", label: "Work", status: "connected" },
    { id: "c2", match: linear, kind: "oauth", label: "Client", status: "connected" },
  ] } }))
  let saved: { url: string; body: unknown } | undefined
  await page.route("**/api/projects/mcp-login*", (route) => {
    saved = { url: route.request().url(), body: route.request().postDataJSON() }
    login = "c2"
    return route.fulfill({ status: 204 })
  })
  await page.goto("/sessions?new=1")
  const loaded = page.getByRole("region", { name: "Loaded in this session" })
  await expect(loaded).toContainText("Logins you pick are saved for agentpack")
  const picker = loaded.getByRole("combobox", { name: "linear login for agentpack" })
  await expect(picker).toContainText("Work")
  await picker.click()
  await page.getByRole("option", { name: /Client/ }).click()
  await expect.poll(() => saved?.body).toEqual({ directory: "/home/oleg/agentpack", url: linear, credential: "c2" })
  expect(new URL(saved!.url).searchParams.get("node")).toBeNull()
  await expect(picker).toContainText("Client")
  await page.reload()
  await expect(page.getByRole("region", { name: "Loaded in this session" }).getByRole("combobox", { name: "linear login for agentpack" })).toContainText("Client")
})

test("a session opens project settings and saves packages, MCP and the login together", async ({ page }) => {
  const directory = sessions[0]!.directory
  const linear = "https://mcp.linear.app/mcp"
  const module = "github.com/acme/linear"
  const project = projectRow(directory, [], {
    mcp: [{ name: "linear", launcher: "remote", source: "plugin", module, url: linear, disabled: true }],
  })
  await page.route((url) => url.pathname === "/api/projects", (route) => route.fulfill({ json: [project] }))
  await page.route("**/api/library/packages", (route) => route.fulfill({ json: [
    { id: "tools", module: "github.com/acme/tools", name: "Tools", kind: "plugin", commit: "abcd1234" },
  ] }))
  await page.route("**/api/mcp/credentials", (route) => route.fulfill({ json: { credentials: [
    { id: "c1", match: linear, kind: "oauth", label: "Work", status: "connected" },
    { id: "c2", match: linear, kind: "oauth", label: "Client", status: "connected" },
  ] } }))
  const saves: unknown[] = []
  await page.route("**/api/projects/settings", (route) => {
    saves.push(route.request().postDataJSON())
    return route.fulfill({ status: 204 })
  })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/sessions")
  const row = page.getByRole("navigation", { name: "Sessions" }).getByRole("link", { name: /build/ })
  await row.click({ button: "right" })
  await expect(page.getByRole("menuitem", { name: /Save agentpack|Install package/ })).toHaveCount(0)
  await page.getByRole("menuitem", { name: "Project settings…" }).click()
  const settings = page.getByRole("dialog", { name: "Project settings" })
  await expect(settings).toContainText(directory)
  await settings.getByRole("textbox", { name: "Search Library packages" }).fill("tools")
  await settings.getByRole("checkbox", { name: "Add Tools" }).check()
  await settings.getByRole("checkbox", { name: "Enable linear" }).check()
  await settings.getByRole("combobox", { name: "linear login" }).click()
  await page.getByRole("option", { name: "Client", exact: true }).click()
  await expect(settings.getByRole("combobox", { name: "linear login" })).toContainText("Client")
  await expect(settings.getByRole("button", { name: "Allow in this project" })).toHaveCount(0)
  await settings.getByRole("button", { name: "Add MCP server…" }).click()
  await settings.getByLabel("Server name").fill("docs")
  await settings.getByLabel("Server URL").fill("https://mcp.example.com/docs")
  expect(saves).toHaveLength(0)
  const save = settings.getByRole("button", { name: "Save", exact: true })
  await expect(save).toBeInViewport()
  await save.click()
  await expect(settings).toBeHidden()
  expect(saves).toEqual([{
    directory, add: ["github.com/acme/tools@abcd1234"], remove: [], remove_mcp: [],
    servers: [{ name: "docs", url: "https://mcp.example.com/docs" }], logins: {},
    plugin_logins: { linear: { module, url: linear, credential: "c2" } },
    mcp_enabled: { linear: true },
  }])
})

test("a project sign-in selects the returned login for Save", async ({ page, context }) => {
  const directory = sessions[0]!.directory
  const url = "https://mcp.linear.app/mcp"
  const module = "github.com/acme/linear"
  let connected = false
  await page.route((url) => url.pathname === "/api/projects", (route) => route.fulfill({ json: [projectRow(directory, [], {
    mcp: [{ name: "linear", source: "plugin", launcher: "remote", module, url }],
  })] }))
  await page.route("**/api/mcp/credentials", (route) => route.fulfill({ json: { credentials: [
    { id: "first", match: url, kind: "oauth", label: "Other team", status: "connected" },
    ...(connected ? [{ id: "new", match: url, kind: "oauth", label: "TrustMind", status: "connected" }] : []),
  ] } }))
  await page.route("**/api/mcp/logins", (route) => route.fulfill({ json: {
    login: "oauth-1", authorize_url: new URL("/test-mcp-oauth", page.url()).href,
  } }))
  await context.route("**/test-mcp-oauth", (route) => {
    connected = true
    return route.fulfill({ contentType: "text/plain", body: "Signed in" })
  })
  await page.route("**/api/mcp/logins/oauth-1", (route) => route.fulfill({ json: connected
    ? { status: "connected", credential_id: "new" } : { status: "pending" } }))
  const saved: unknown[] = []
  await page.route("**/api/projects/settings", (route) => {
    saved.push(route.request().postDataJSON())
    return route.fulfill({ status: 204 })
  })
  await page.goto("/sessions")
  await page.getByRole("navigation", { name: "Sessions" }).getByRole("link", { name: /build/ }).click({ button: "right" })
  await page.getByRole("menuitem", { name: "Project settings…" }).click()
  const settings = page.getByRole("dialog", { name: "Project settings" })
  await settings.getByRole("button", { name: "Sign in…" }).click()
  await expect(settings.getByRole("combobox", { name: "linear login" })).toContainText("TrustMind")
  expect(saved).toHaveLength(0)
  await settings.getByRole("button", { name: "Save", exact: true }).click()
  await expect(settings).toBeHidden()
  expect(saved).toEqual([expect.objectContaining({ plugin_logins: { linear: { module, url, credential: "new" } } })])
})

test("+ opens the composer while the chat claim is still in flight", async ({ page }) => {
  let posted: { id: string } | undefined
  let release!: () => void
  const heldResponse = new Promise<void>((resolve) => { release = resolve })
  const starting = { ...sessions[1], transport: "acp", conversation_state: "starting", state: terminal(false) }
  await page.route(/\/api\/sessions(\?.*)?$/, async (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    posted = route.request().postDataJSON()
    await heldResponse
    const info = { ...starting, id: posted!.id }
    reply = [...sessions, info]
    return route.fulfill({ json: info })
  })
  await page.route("**/api/agent-snapshot?*", (route) => route.fulfill({
    json: { config: { configOptions: [
      { type: "select", id: "model", name: "Model", category: "model", currentValue: "codex", options: [{ value: "codex", name: "Codex" }] },
    ] } },
  }))
  const prompts: Array<{ body: unknown; id?: string }> = []
  await page.route("**/api/sessions/*/conversation/prompt", (route) => {
    const id = route.request().headers()["idempotency-key"]
    prompts.push({ body: route.request().postDataJSON(), id })
    return route.fulfill({ json: { accepted: true, queued: id } })
  })
  await page.goto("/sessions")
  await page.getByRole("button", { name: "New Codex chat in oracle on host-a" }).click()
  await expect.poll(() => posted).toMatchObject({
    id: expect.stringMatching(/^session-[0-9a-f]{32}$/),
    directory: "/home/oleg/oracle", harness: "codex", mode: "plan", transport: "acp",
  })
  await expect(page).toHaveURL(new RegExp(`/sessions/[^/]+/${posted!.id}$`))
  await expect(page.getByRole("dialog")).toBeHidden()
  const composer = page.getByRole("textbox", { name: "Message" })
  await expect(composer).toHaveAttribute("placeholder", "Starting Codex…")
  await expect(page.getByRole("button", { name: "Model", exact: true })).toHaveText("Codex")
  await composer.fill("check the budgets")
  await composer.press("Enter")
  await expect(page.getByText("check the budgets", { exact: true })).toBeVisible()
  await expect(composer).toHaveValue("")
  await composer.fill("then confirm the results")
  await composer.press("Enter")
  await expect(page.getByText("then confirm the results", { exact: true })).toBeVisible()
  expect(prompts).toHaveLength(0)
  release()
  await expect.poll(() => prompts.map(({ body }) => body)).toEqual([
    { text: "check the budgets" }, { text: "then confirm the results" },
  ])
  expect(new Set(prompts.map(({ id }) => id)).size).toBe(2)
})

test("an image can be attached before a chat claim and sent while its agent is starting", async ({ page }) => {
  let release!: () => void
  const heldResponse = new Promise<void>((resolve) => { release = resolve })
  await page.route(/\/api\/sessions(\?.*)?$/, async (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    const { id } = route.request().postDataJSON()
    await heldResponse
    const info = { ...sessions[1], id, transport: "acp", conversation_state: "starting", state: terminal(false) }
    reply = [...sessions, info]
    return route.fulfill({ json: info })
  })
  const sha = "a".repeat(64)
  await page.route("**/api/sessions/*/conversation/blobs", (route) => route.fulfill({ json: { sha } }))
  let posted: unknown
  await page.route("**/api/sessions/*/conversation/prompt/images", (route) => {
    posted = route.request().postDataJSON()
    return route.fulfill({ json: { queued: route.request().headers()["idempotency-key"] } })
  })
  await page.goto("/sessions")
  await page.getByRole("button", { name: "New Codex chat in oracle on host-a" }).click()
  await expect(page.getByRole("button", { name: "Attach image" })).toBeEnabled()
  await page.locator('input[type="file"]').setInputFiles({
    name: "screen.png", mimeType: "image/png", buffer: conversationImage,
  })
  await expect(page.getByText("Attaching screen.png…")).toBeVisible()
  release()
  await expect(page.getByText("Image attached · screen.png")).toBeVisible()
  await expect(page.getByPlaceholder("Starting Codex…")).toBeVisible()
  await page.getByRole("button", { name: "Send", exact: true }).click()
  await expect.poll(() => posted).toMatchObject({ images: [{ sha, name: "screen.png" }] })
})

test("⌥-click on + lists the agents and can start one in a new worktree", async ({ page }) => {
  let posted: unknown
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    posted = route.request().postDataJSON()
    return route.fulfill({ status: 500, body: "stop here" })
  })
  await page.goto("/sessions")
  const plus = page.getByRole("button", { name: "New Codex chat in oracle on host-a" })
  await plus.click({ modifiers: ["Alt"] })
  await expect(page.getByRole("menuitem", { name: "New Claude chat" })).toBeVisible()
  await expect(page).not.toHaveURL(/\/sessions\/[^/]+\/s/)
  const worktree = page.getByRole("menuitemcheckbox", { name: "Run in worktree" })
  await expect(worktree).not.toBeChecked()
  await worktree.click()
  await expect(worktree).toBeChecked()
  await page.getByRole("menuitem", { name: "New Claude chat" }).click()
  await expect.poll(() => posted).toMatchObject({ harness: "claude", worktree: "new" })
  // Off again the next time the menu opens.
  await plus.click({ modifiers: ["Alt"] })
  await expect(page.getByRole("menuitemcheckbox", { name: "Run in worktree" })).not.toBeChecked()
})

test("closing New session drops the folder it was opened for", async ({ page }) => {
  await page.goto("/sessions?new=1&machine=local&folder=%2Fhome%2Foleg%2Foracle")
  await expect(page.getByRole("dialog").getByRole("heading", { name: "oracle" })).toBeVisible()
  await page.keyboard.press("Escape")
  await expect(page.getByRole("dialog")).toBeHidden()
  await expect(page).not.toHaveURL(/folder=|machine=|new=/)
})

test("Refresh rereads sessions", async ({ page }) => {
  await page.goto("/sessions")
  const list = page.getByRole("navigation", { name: "Sessions" })
  await expect(list.locator("[data-source-row]")).toHaveCount(2)

  reply = renamed
  const before = fetches
  await page.getByRole("button", { name: "Refresh sessions" }).click()
  await expect.poll(() => fetches).toBeGreaterThan(before)
  await expect(page.getByRole("link", { name: /oracle main/ })).toBeVisible()

  await page.unroute("**/api/nodes")
  await page.route("**/api/nodes", (route) => route.fulfill({ status: 400, json: { error: "Tailscale is unavailable" } }))
  await page.getByRole("button", { name: "Refresh sessions" }).click()
  await expect(list.getByRole("alert")).toHaveText(/Can't look for other machines: Tailscale is unavailable/)
})

test("a reload paints the last-known list before the gateway answers", async ({ page }) => {
  await page.clock.install()
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows).toHaveCount(2)
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")))
  await page.waitForFunction(() => new Promise((resolve, reject) => {
    const opening = indexedDB.open("agentpack-cache", 2)
    opening.onerror = () => reject(opening.error)
    opening.onsuccess = () => {
      const db = opening.result
      const transaction = db.transaction("entries")
      const request = transaction.objectStore("entries").get('["m","local","sessions"]')
      transaction.oncomplete = () => {
        db.close()
        resolve(request.result !== undefined)
      }
      transaction.onerror = () => reject(transaction.error)
    }
  }))
  // An entry from another build is dropped on the next restore.
  const entry = (write: boolean) => page.evaluate((write) => new Promise((resolve, reject) => {
    const opening = indexedDB.open("agentpack-cache", 2)
    opening.onerror = () => reject(opening.error)
    opening.onsuccess = () => {
      const db = opening.result
      const transaction = db.transaction("entries", write ? "readwrite" : "readonly")
      const store = transaction.objectStore("entries")
      const request = write ? store.put({ build: "old", at: Date.now(), chat: 0, query: { queryKey: ["status"], state: {} } }, "stale") : store.get("stale")
      transaction.oncomplete = () => {
        db.close()
        resolve(request.result !== undefined)
      }
      transaction.onerror = () => reject(transaction.error)
    }
  }), write)
  await entry(true)
  await page.route(/\/api\/sessions(\?.*)?$/, () => undefined)
  await page.reload()
  await expect(rows).toHaveCount(2)
  await expect.poll(() => entry(false)).toBe(false)
})

test("⌘/Ctrl- and Shift-click mark rows; the menu then acts on all of them", async ({ page }) => {
  const archived: string[] = []
  await page.route("**/api/sessions/*", (route) => {
    if (route.request().method() !== "PATCH") return route.fallback()
    archived.push(new URL(route.request().url()).pathname.split("/").pop()!)
    return route.fulfill({ json: {} })
  })
  await page.route("**/api/sessions/*/stop", (route) => route.fulfill({ json: { stopped: true } }))
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await rows.first().click({ modifiers: ["ControlOrMeta"] })
  await rows.last().click({ modifiers: ["Shift"] })
  // Marking never opens a session.
  await expect(page).toHaveURL(/\/sessions(\?|$)/)
  await expect(rows.and(page.locator("[data-marked]"))).toHaveCount(2)

  await rows.last().click({ button: "right" })
  // Only s1 is running, so the stop counts one; both can be archived.
  await expect(page.getByRole("menuitem", { name: "Stop agent…" })).toBeEnabled()
  // Archive asks nothing: it is the Trash of the session list.
  await page.getByRole("menuitem", { name: /^Archive 2 sessions/ }).click()
  await expect(page.getByRole("alertdialog")).toHaveCount(0)
  await expect.poll(() => archived.sort()).toEqual(["s1", "s2"])
  await expect(page.locator("[data-marked]")).toHaveCount(0)
})

test("archive hides the row immediately and restores it if the request fails", async ({ page }) => {
  const intentIDs: Array<string | undefined> = []
  let release!: () => void
  const response = new Promise<void>((resolve) => { release = resolve })
  let releaseRetry!: () => void
  const retryResponse = new Promise<void>((resolve) => { releaseRetry = resolve })
  await page.route("**/api/sessions/*", async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback()
    intentIDs.push(route.request().headers()["idempotency-key"])
    await (intentIDs.length === 1 ? response : retryResponse)
    await route.fulfill({ status: 409, body: "Still running" })
  })
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows).toHaveCount(2)
  await rows.last().click({ button: "right" })
  await page.getByRole("menuitem", { name: /^Archive/ }).click()
  await expect(rows).toHaveCount(1)
  release()
  await expect(rows).toHaveCount(2)
  await page.getByRole("button", { name: "Retry", exact: true }).click()
  await expect.poll(() => intentIDs.length).toBe(2)
  expect(intentIDs[0]).toMatch(/^[a-f0-9]{32}$/)
  expect(intentIDs[1]).toBe(intentIDs[0])
  await expect(rows).toHaveCount(1)
  releaseRetry()
  await expect(rows).toHaveCount(2)
})

test("a chat's row menu switches agentpack tools, and its helpers list under it", async ({ page }) => {
  const chat = { ...sessions[0], id: "c1", name: "lead", transport: "acp", conversation_state: "idle", tools: true }
  const helper = { ...chat, id: "c2", name: "helper", parent_id: "c1", conversation_state: "working", started_by: { kind: "browser", user: "oleg@", agent: "claude" } }
  reply = [helper, sessions[1], chat]
  const bodies: unknown[] = []
  await page.route("**/api/sessions/c1/tools", (route) => {
    bodies.push(route.request().postDataJSON())
    return route.fulfill({ json: { tools: false } })
  })
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await expect(rows.filter({ hasText: /lead|helper/ })).toHaveText([/lead/, /helper/])
  await rows.filter({ hasText: "lead" }).click({ button: "right" })
  const toggle = page.getByRole("menuitemcheckbox", { name: "agentpack Tools" })
  await expect(toggle).toHaveAttribute("aria-checked", "true")
  await toggle.click()
  await expect.poll(() => bodies).toEqual([{ enabled: false }])
})

test("Esc clears the marked rows", async ({ page }) => {
  await page.goto("/sessions")
  const rows = page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
  await rows.first().click({ modifiers: ["ControlOrMeta"] })
  await rows.last().click({ modifiers: ["ControlOrMeta"] })
  await expect(page.locator("[data-marked]")).toHaveCount(2)
  await page.keyboard.press("Escape")
  await expect(page.locator("[data-marked]")).toHaveCount(0)
})

test("Sessions home answers a waiting chat's permission, then ⌘⇧J and ⌘K jump", async ({ page }) => {
  const waiting = { ...sessions[0], id: "c1", name: "refactor staging", transport: "acp", conversation_state: "needs_you" }
  reply = [waiting, sessions[1]]
  const request = '{"seq":1,"ts":1,"kind":"permission_request","payload":{"requestId":"p1","tool":"Bash","summary":"go test ./...","options":[{"id":"allow","label":"Allow once"},{"id":"deny","label":"Deny"}]}}\n'
  await page.route("**/api/sessions/c1/conversation", (route) => route.fulfill({ body: request, contentType: "application/x-ndjson" }))
  const answers: unknown[] = []
  await page.route("**/api/sessions/c1/conversation/permission", (route) => {
    answers.push(route.request().postDataJSON())
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions")
  const waitingSection = page.getByRole("region", { name: "Needs you" })
  await expect(waitingSection).toContainText("refactor staging")
  await waitingSection.getByRole("button", { name: /Allow once/ }).click()
  await expect.poll(() => answers).toEqual([{ request_id: "p1", option_id: "allow" }])

  await page.keyboard.press("ControlOrMeta+Shift+j")
  await expect(page).toHaveURL(/\/sessions\/local\/c1$/)
  await page.keyboard.press("ControlOrMeta+k")
  await page.getByRole("combobox", { name: "Search sessions and commands" }).fill("oracle")
  await page.keyboard.press("Enter")
  await expect(page).toHaveURL(/\/sessions\/local\/s2$/)
})

test("New worktree starts the chat on its own branch and brings uncommitted changes", async ({ page }) => {
  const project = { directory: "/home/oleg/agentpack", name: "agentpack", folder_id: "f1", available: true, unavailable_reason: "", last_used: twoHoursAgo, launches: [], packages: [], mcp: [], error: "", git: { branch: "main", head: "abc", dirty: 2 } }
  await page.route("**/api/projects", (route) => route.fulfill({ json: [project] }))
  await page.route("**/api/worktrees", (route) => route.fulfill({ json: { git: true, worktrees: [] } }))
  await page.goto("/sessions?new=1")
  // s1 already runs in this folder, so a new worktree is the default.
  await expect(page.getByText("1 other agent is working in agentpack.", { exact: false })).toBeVisible()
  await page.getByRole("textbox", { name: "First message" }).fill("Fix the cache")
  await expect(page.getByText("Branch ap/fix-the-cache from main.", { exact: false })).toBeVisible()
  await expect(page.getByRole("checkbox", { name: /Bring 2 uncommitted changes/ })).toBeChecked()
  const posted = page.waitForRequest((request) => request.method() === "POST" && /\/api\/sessions$/.test(request.url()))
  await page.getByRole("button", { name: "Start session" }).click()
  expect((await posted).postDataJSON()).toMatchObject({ directory: "/home/oleg/agentpack", worktree: "new", worktree_name: "Fix the cache", carry: true })
})

test("New worktree reads Needs update on a gateway without worktrees", async ({ page }) => {
  const project = { directory: "/home/oleg/agentpack", name: "agentpack", folder_id: "f1", available: true, unavailable_reason: "", last_used: twoHoursAgo, launches: [], packages: [], mcp: [], error: "" }
  await page.route("**/api/projects", (route) => route.fulfill({ json: [project] }))
  await page.route("**/api/worktrees", (route) => route.fulfill({ status: 404, json: { error: "not found" } }))
  await page.goto("/sessions?new=1")
  await expect(page.getByText("New worktree · Needs update")).toBeVisible()
  const posted = page.waitForRequest((request) => request.method() === "POST" && /\/api\/sessions$/.test(request.url()))
  await page.getByRole("button", { name: "Start session" }).click()
  expect((await posted).postDataJSON()).not.toHaveProperty("worktree")
})
