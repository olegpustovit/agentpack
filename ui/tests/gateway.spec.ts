import { expect, test, type Locator, type Page, type WebSocketRoute } from "@playwright/test"
import { attachTerminal, mockAPI, projectRow, session } from "./helpers"

// Rows carry data-row-key="<machine>:<session id>"; local rows use the machine id "local".
const gwRow = (page: Page, key: string) => page.locator(`[data-source-row][data-row-key="${key}"]`)
const gwMore = (page: Page, key: string) => gwRow(page, key).locator("xpath=..").getByRole("button", { name: "More actions" })
const gwStatus = (page: Page) => page.locator('[data-slot="status-bar"]')
// The terminal stage is the only element with aria-busy on the session route.
const gwStage = (page: Page) => page.locator("div[aria-busy]")
const gwSessions = (page: Page) => page.getByRole("navigation", { name: "Sessions" })
// The toolbar gear comes before the phone tab bar's Settings link in the DOM.
const gwSettingsLink = (page: Page) => page.getByRole("link", { name: "Settings", exact: true }).first()
const gwDesktopViewport = { width: 1280, height: 800 }
// Reconnects add a query string (?since=), so the socket route must match it too.
const gwTerminalSocket = "**/api/sessions/*/terminal*"

// Machine refresh lives in the status bar's connection popover, which only shows on desktop widths.
async function gwRefresh(page: Page) {
  await page.getByRole("button", { name: /^Connected/ }).click()
  await page.getByRole("button", { name: "Refresh", exact: true }).click()
  await page.keyboard.press("Escape")
}

// Detaches the open terminal. Phones hide the row menu behind the terminal, so Back does it there;
// on desktop the row's Close terminal does it.
async function gwDetach(page: Page, key: string) {
  const back = page.getByRole("link", { name: "Back", exact: true })
  if (await back.isVisible()) {
    await back.click()
    return
  }
  await gwMore(page, key).click()
  await page.getByRole("menuitem", { name: "Close terminal" }).click()
}


// The whole xterm buffer as text, so assertions hold for the DOM and WebGL renderers alike.
async function gwBuffer(page: Page): Promise<string> {
  return page.evaluate(() => {
    const buffer = window.__agentpackTerminal?.buffer.active
    if (!buffer) return ""
    const lines: string[] = []
    for (let index = 0; index < buffer.length; index++) lines.push(buffer.getLine(index)?.translateToString(true) ?? "")
    return lines.join("\n")
  })
}

// Count of writes to the sidebar layout key, installed before the app loads.
async function gwLayoutWrites(page: Page): Promise<number> {
  return page.evaluate(() => Reflect.get(window, "gwLayoutWrites").count)
}

async function gwWidth(locator: Locator): Promise<number> {
  return (await locator.boundingBox())?.width ?? 0
}

// Reads a request body's string or boolean field without casting.
function gwString(body: unknown, key: string): string {
  if (typeof body !== "object" || body === null) return ""
  const value: unknown = new Map<string, unknown>(Object.entries(body)).get(key)
  return typeof value === "string" ? value : ""
}

function gwFlag(body: unknown, key: string): boolean {
  if (typeof body !== "object" || body === null) return false
  return new Map<string, unknown>(Object.entries(body)).get(key) === true
}

const gwText = (value: string | Buffer) => (typeof value === "string" ? value : value.toString())
const gwCtrlC = (value: string | Buffer) => typeof value !== "string" && Buffer.from(value).equals(Buffer.from([3]))

// A socket the test expects to exist; a missing one fails here with a clear message.
function gwSocket(sockets: WebSocketRoute[], index: number): WebSocketRoute {
  const ws = sockets[index]
  if (!ws) throw new Error(`terminal socket ${index} was never opened`)
  return ws
}

// Library pages read drafts and replication, which mockAPI leaves empty, so these stubs answer with the real shapes.
async function gwLibraryStubs(page: Page) {
  await page.route((url) => url.pathname === "/api/library/drafts", (route) => route.fulfill({ json: [] }))
  await page.route((url) => url.pathname === "/api/library/replication/peers", (route) => route.fulfill({ json: { peers: [], contacts: {} } }))
}

test("host grid stays fixed while fit, zoom and browser size change", async ({ page }) => {
  const requests = await mockAPI(page)
  const controls = await attachTerminal(page)
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await expect(page.locator(".xterm-screen")).toBeVisible()
  await expect(gwStatus(page)).toContainText("80 × 24")

  await page.setViewportSize({ width: 1024, height: 700 })
  await page.getByRole("button", { name: "1:1" }).click()
  await expect(page.getByText("100%", { exact: true })).toBeVisible()
  const actual = await page.evaluate(() => window.__agentpackTerminal?.options.fontSize ?? 0)
  await page.getByRole("button", { name: "Larger" }).click()
  await expect(page.getByText("108%", { exact: true })).toBeVisible()
  expect(await page.evaluate(() => window.__agentpackTerminal?.options.fontSize ?? 0)).toBeGreaterThan(actual)
  await page.getByRole("button", { name: "Fit" }).click()

  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.waitForTimeout(100)
  await expect(gwStatus(page)).toContainText("80 × 24")
  // Zoom changes the font, never the grid, and the stage is never scaled with a CSS transform.
  expect(await gwStage(page).evaluate((el) => getComputedStyle(el).transform)).toBe("none")
  expect(await page.locator(".xterm").evaluate((el) => getComputedStyle(el).transform)).toBe("none")
  expect(controls.some((value) => typeof value === "string" && value.includes("resize"))).toBeFalsy()
  expect(requests.some((value) => value.path === "/api/sessions" && value.query.has("node"))).toBeFalsy()
})

test("sidebar stays left, resizes, collapses and selection follows arrow keys", async ({ page }) => {
  await page.setViewportSize(gwDesktopViewport)
  // Counts writes to the sidebar layout key, so the debounce is measured directly.
  await page.addInitScript(() => {
    const writes = { count: 0 }
    Object.defineProperty(window, "gwLayoutWrites", { value: writes })
    const setItem = Storage.prototype.setItem
    Storage.prototype.setItem = function (key: string, value: string) {
      if (key.startsWith("react-resizable-panels:agentpack-sidebar")) writes.count++
      setItem.call(this, key, value)
    }
  })
  await mockAPI(page)
  await attachTerminal(page)
  await page.goto("/")
  const sidebar = page.getByRole("region", { name: "Sidebar" })
  const content = page.locator("[data-panel]").last()
  expect(await sidebar.evaluate((el) => el.getBoundingClientRect().left)).toBeLessThan(await content.evaluate((el) => el.getBoundingClientRect().left))

  const handle = page.getByRole("separator")
  const box = await handle.boundingBox()
  if (!box) throw new Error("sidebar splitter is not rendered")
  const writesBefore = await gwLayoutWrites(page)
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(340, box.y + box.height / 2, { steps: 12 })
  await page.mouse.up()
  await expect.poll(async () => Math.abs((await gwWidth(sidebar)) - 340) <= 8).toBe(true)
  // The layout is saved once when the drag settles, not once per pointer move.
  await expect.poll(async () => (await gwLayoutWrites(page)) - writesBefore).toBe(1)
  await page.waitForTimeout(300)
  expect((await gwLayoutWrites(page)) - writesBefore).toBe(1)

  // The width survives a reload.
  await page.reload()
  await expect.poll(async () => Math.abs((await gwWidth(sidebar)) - 340) <= 8).toBe(true)

  await gwRow(page, "local:local-one").click()
  // Attaching the terminal takes focus, so the list is focused again before the arrow key.
  await expect(gwStatus(page)).toContainText("Shared input")
  await gwRow(page, "local:local-one").focus()
  // The list sorts folders by name, so another-project sits above my-project.
  await page.keyboard.press("ArrowUp")
  await expect(page).toHaveURL(/\/sessions\/local\/local-two$/)

  await page.getByRole("button", { name: "Toggle sidebar" }).click()
  await expect(page.getByRole("button", { name: "Toggle sidebar" })).toHaveAttribute("aria-expanded", "false")
  await expect.poll(() => gwWidth(sidebar)).toBe(0)
  // The collapsed state survives a reload.
  await page.reload()
  await expect(page.getByRole("button", { name: "Toggle sidebar" })).toHaveAttribute("aria-expanded", "false")
  await expect.poll(() => gwWidth(sidebar)).toBe(0)
})

test("session hierarchy has icons and keeps collapsed groups through refresh", async ({ page }) => {
  // shortcut: the React list has no collapsible groups, so only the hierarchy and its refresh survival are ported.
  await page.setViewportSize(gwDesktopViewport)
  await mockAPI(page)
  await page.goto("/")
  const group = page.locator("nav > div").filter({ has: page.locator('[data-slot="group-header"]', { hasText: "Gateway machine" }) })
  const subhead = group.locator('[data-slot="group-header"]').filter({ hasText: "my-project" })
  const row = gwRow(page, "local:local-one")
  await expect(row).toBeVisible()
  await expect(row.getByRole("img", { name: "Running" })).toBeVisible()
  await expect(subhead).toBeVisible()
  const indent = async () => {
    const rowLeft = await row.getByText("Claude", { exact: true }).evaluate((el) => el.getBoundingClientRect().left)
    const headLeft = await subhead.getByText("my-project", { exact: true }).evaluate((el) => el.getBoundingClientRect().left)
    return rowLeft - headLeft
  }
  await expect.poll(indent).toBeGreaterThan(0)

  await gwRefresh(page)
  await expect(subhead).toBeVisible()
  await expect(row).toBeVisible()
  await expect.poll(indent).toBeGreaterThan(0)
})

test("context rename saves on the correct host and survives refresh and reload", async ({ page }) => {
  await page.setViewportSize(gwDesktopViewport)
  await mockAPI(page)
  await attachTerminal(page)
  let name = ""
  let renamedBody: unknown
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "peer-1", name: "Build server", online: true }] }))
  await page.route((url) => url.pathname === "/api/sessions" && url.searchParams.get("node") === "peer-1", (route) =>
    route.fulfill({ json: [{ ...session, id: "remote-one", name }] }),
  )
  await page.route((url) => url.pathname === "/api/sessions/remote-one" && url.searchParams.get("node") === "peer-1", (route) => {
    renamedBody = route.request().postDataJSON()
    name = gwString(renamedBody, "name")
    return route.fulfill({ json: { ...session, id: "remote-one", name } })
  })
  await page.goto("/")
  const row = gwRow(page, "peer-1:remote-one")
  await row.click()
  await row.click({ button: "right" })
  await page.getByRole("menuitem", { name: "Rename…" }).click()
  await page.getByLabel("Name", { exact: true }).fill("Fix sidebar")
  await page.getByLabel("Name", { exact: true }).press("Enter")
  await expect(page.getByRole("dialog")).toBeHidden()
  await expect(row).toContainText("Fix sidebar")
  expect(renamedBody).toEqual({ name: "Fix sidebar" })

  await gwRefresh(page)
  await expect(row).toContainText("Fix sidebar")
  await page.reload()
  await expect(row).toContainText("Fix sidebar")
})

test("row actions rename without attaching, preserve errors, cancel, and stop the chosen session", async ({ page }) => {
  const requests = await mockAPI(page)
  let attempts = 0
  await page.route("**/api/sessions/local-two", (route) => {
    if (route.request().method() !== "PATCH") return route.fallback()
    attempts++
    return route.fulfill({ status: 500, body: "Could not save the name" })
  })
  await page.goto("/")
  await gwMore(page, "local:local-two").click()
  await page.getByRole("menuitem", { name: "Rename…" }).click()
  await page.getByLabel("Name", { exact: true }).fill("Keep this draft")
  await page.getByRole("button", { name: "Rename", exact: true }).click()
  await expect(page.getByRole("alert")).toHaveText("Could not save the name")
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("Keep this draft")
  await page.keyboard.press("Escape")
  await expect(page.getByRole("dialog")).toBeHidden()
  expect(attempts).toBe(1)
  // Renaming never opens the terminal.
  await expect(page).toHaveURL(/\/sessions$/)

  const row = gwRow(page, "local:local-two")
  await row.click({ button: "right" })
  await page.getByRole("menuitem", { name: "Stop…" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Stop agent" }).click()
  await expect.poll(() => requests.some((item) => item.path === "/api/sessions/local-two/stop")).toBeTruthy()
  expect(requests.some((item) => item.path === "/api/sessions/local-one/stop")).toBeFalsy()

  await row.focus()
  await page.keyboard.press("Shift+F10")
  await expect(page.getByRole("menuitem", { name: "Rename…" })).toBeFocused()
  await page.keyboard.press("Escape")
  await expect(page.getByRole("menu")).toBeHidden()
  await expect(row).toBeFocused()
})

test("legacy worker stays read-only and offers no control toggle", async ({ page }) => {
  await mockAPI(page)
  const received = await attachTerminal(page, { terminalState: { shared_input: false, responder: 0 } })
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await expect(page.getByText("Start a new session to enable shared input.")).toBeVisible()
  await expect(gwStatus(page)).not.toContainText("Shared input")
  await expect(page.getByRole("button", { name: /Take control|Release control/ })).toHaveCount(0)
  await page.locator(".xterm-screen").click()
  await page.keyboard.press("Control+C")
  expect(received.some(gwCtrlC)).toBeFalsy()
})

test("390px switches between the session list and terminal panes", async ({ page }) => {
  await mockAPI(page)
  await attachTerminal(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/")
  await expect(gwSessions(page)).toBeVisible()
  await gwRow(page, "local:local-one").click()
  await expect(page.locator(".xterm-screen")).toBeVisible()
  await page.getByRole("link", { name: "Back", exact: true }).click()
  await expect(gwSessions(page)).toBeVisible()
  await expect(page.locator(".xterm-screen")).toBeHidden()
  await gwMore(page, "local:local-one").click()
  await page.getByRole("menuitem", { name: "Rename…" }).click()
  await expect(page.getByLabel("Name", { exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Cancel", exact: true }).click()
  await gwRow(page, "local:local-one").click()
  await expect(page.locator(".xterm-screen")).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
})

test("new session only offers recent folders and routes start to the chosen machine", async ({ page }) => {
  const requests = await mockAPI(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "peer-1", name: "Build server", online: true }] }))
  await page.route((url) => url.pathname === "/api/projects" && url.searchParams.get("node") === "peer-1", (route) =>
    route.fulfill({ json: [projectRow("/srv/app", [{ harness: "claude" }])] }),
  )
  await page.route((url) => url.pathname === "/api/sessions" && url.searchParams.get("node") === "peer-1", (route) => route.fulfill({ json: [] }))
  await page.goto("/")
  await page.getByRole("link", { name: "New session", exact: true }).click()
  await page.getByRole("combobox", { name: "Project" }).click()
  await expect(page.getByRole("option")).toHaveText(["/work/previous"])
  await page.getByRole("option", { name: "/work/previous" }).click()
  await page.getByRole("button", { name: "Start session" }).click()
  await expect(page.getByRole("dialog")).toBeHidden()
  expect(requests).toContainEqual(expect.objectContaining({ method: "POST", path: "/api/sessions", body: expect.objectContaining({ directory: "/work/previous" }) }))

  // A successful start opens the new session, which on phones hides the list.
  await page.goto("/sessions")
  await page.getByRole("link", { name: "New session", exact: true }).click()
  await page.getByRole("combobox", { name: "Machine" }).click()
  await page.getByRole("option", { name: "Build server" }).click()
  await expect(page.getByRole("combobox", { name: "Project" })).toContainText("/srv/app")
  const posted = page.waitForRequest((request) => request.method() === "POST" && new URL(request.url()).searchParams.get("node") === "peer-1")
  await page.getByRole("button", { name: "Start session" }).click()
  expect((await posted).postDataJSON()).toMatchObject({ directory: "/srv/app" })
})

test("shared input works without ownership and detaching never stops the agent", async ({ page }) => {
  const requests = await mockAPI(page)
  const received = await attachTerminal(page)
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await expect(gwStatus(page)).toContainText("Shared input")
  await page.locator(".xterm-screen").click()
  await page.keyboard.press("Control+C")
  await expect.poll(() => received.some(gwCtrlC)).toBeTruthy()
  await expect(page.getByRole("button", { name: /Take control|Release control/ })).toHaveCount(0)
  await gwDetach(page, "local:local-one")
  expect(requests.some((value) => value.path.endsWith("/stop"))).toBeFalsy()
})

test("replay blocks input and query replies until rendered, then only the responder answers", async ({ page }) => {
  const received = await attachTerminal(page, { terminalState: { responder: 7 }, holdReplay: true, initialQuery: "\x1b[6n" })
  await mockAPI(page)
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "true")
  await page.locator(".xterm-screen").click()
  await page.keyboard.press("Control+C")
  await page.waitForTimeout(50)
  expect(received).toHaveLength(0)

  gwSocket(received.sockets, 0).send(JSON.stringify({ type: "replay_complete" }))
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "false")
  // Responder 7 is not this viewer, so the query gets no reply and input is shared.
  gwSocket(received.sockets, 0).send(Buffer.from("\x1b[6n"))
  await page.locator(".xterm-screen").click()
  await page.keyboard.press("Control+C")
  await expect.poll(() => received.some(gwCtrlC)).toBeTruthy()
  expect(received.some((value) => gwText(value).includes("\x1b[2;1R"))).toBeFalsy()
  await page.locator(".xterm-helper-textarea").evaluate((el) => {
    const clipboard = new DataTransfer()
    clipboard.setData("text/plain", "paste")
    el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: clipboard, bubbles: true }))
  })
  await expect.poll(() => received.some((value) => gwText(value).includes("paste"))).toBeTruthy()

  gwSocket(received.sockets, 0).send(JSON.stringify({ type: "state", state: { ...session.state, shared_input: true, responder: 42 } }))
  gwSocket(received.sockets, 0).send(Buffer.from("\x1b[6n"))
  await expect.poll(() => received.some((value) => gwText(value).includes("\x1b[2;1R"))).toBeTruthy()
})

test("clicking the selected row preserves its terminal and compact menu aligns icons and labels", async ({ page }) => {
  await page.setViewportSize(gwDesktopViewport)
  await mockAPI(page)
  const sockets: WebSocketRoute[] = []
  await page.routeWebSocket(gwTerminalSocket, (ws) => {
    sockets.push(ws)
    ws.send(JSON.stringify({ type: "attached", attachment: 42, state: { ...session.state, shared_input: true, responder: 42 } }))
    ws.send(JSON.stringify({ type: "replay_complete" }))
  })
  await page.goto("/")
  const row = gwRow(page, "local:local-one")
  await row.click()
  await expect(gwStatus(page)).toContainText("Shared input")
  await page.getByRole("button", { name: "1:1" }).click()
  const terminal = await page.locator(".xterm").elementHandle()
  if (!terminal) throw new Error("terminal is not mounted")
  const scale = await page.getByText(/^\d+%$/).textContent()
  await row.click()
  await expect.poll(() => sockets.length).toBe(1)
  expect(await page.evaluate((el) => el === document.querySelector(".xterm"), terminal)).toBeTruthy()
  await expect(page.getByText(/^\d+%$/)).toHaveText(scale ?? "")

  await gwMore(page, "local:local-one").click()
  const item = page.getByRole("menuitem", { name: "Rename…" })
  const icon = await item.locator("svg").boundingBox()
  const label = await item.locator("span").boundingBox()
  const menuRow = await item.boundingBox()
  if (!icon || !label || !menuRow) throw new Error("menu item is not laid out")
  // The icon sits at the start of the row, to the left of its label.
  expect(icon.x - menuRow.x).toBeLessThan(16)
  expect(icon.x).toBeLessThan(label.x)
  await expect(page.getByRole("menuitem", { name: /Take control|Release control/ })).toHaveCount(0)
  await page.keyboard.press("Escape")

  // Another row opens its own terminal; the first one stays pooled.
  await expect(page.getByRole("menuitem", { name: "Rename…" })).toBeHidden()
  await gwMore(page, "local:local-two").click()
  await page.getByRole("menuitem", { name: "Reconnect" }).click()
  await expect.poll(() => sockets.length).toBe(2)
  await expect(gwStatus(page)).toContainText("Codex")

  // Reconnect on the open row must open a new connection and keep its terminal element.
  // The previous menu is still closing for its exit animation; wait for it, or two Reconnect items match.
  await expect(page.getByRole("menu")).toBeHidden()
  await gwMore(page, "local:local-one").click()
  await page.getByRole("menuitem", { name: "Reconnect" }).click()
  await expect.poll(() => sockets.length).toBe(3)
  expect(await page.evaluate((el) => el === document.querySelector(".xterm"), terminal)).toBeTruthy()
})

test("backend strings are text and API failures stay visible", async ({ page }) => {
  await mockAPI(page)
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [{ ...session, directory: "/work/<img src=x onerror=alert(1)>" }] }))
  await page.route("**/api/packages", (route) => route.fulfill({ status: 500, json: { error: "Project is not available" } }))
  await page.goto("/")
  await expect(gwSessions(page)).toContainText("<img src=x onerror=alert(1)>")
  await expect(page.locator("img")).toHaveCount(0)
  await gwMore(page, "local:local-one").click()
  await page.getByRole("menuitem", { name: "Install package…" }).click()
  await page.getByLabel("Package", { exact: true }).fill("acme/skill")
  await page.getByRole("button", { name: "Install", exact: true }).click()
  await expect(page.getByRole("alert")).toContainText("Project is not available")
})

test("peer sessions load independently and terminal links retain the node", async ({ page }) => {
  await mockAPI(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "n-main", name: "Build server", online: true }] }))
  // The peer's list is held until the local list is on screen, which shows the peer does not block it.
  let releasePeer: () => void = () => {}
  const peerReady = new Promise<void>((resolve) => {
    releasePeer = resolve
  })
  await page.route((url) => url.pathname === "/api/sessions" && url.searchParams.get("node") === "n-main", async (route) => {
    await peerReady
    await route.fulfill({ json: [{ ...session, id: "remote-one", directory: "/srv/project" }] })
  })
  const urls: string[] = []
  await page.routeWebSocket(/\/api\/sessions\/[^/]+\/terminal/, (ws) => {
    urls.push(ws.url())
    ws.send(JSON.stringify({ type: "attached", attachment: 42, state: { ...session.state, shared_input: true, responder: 42 } }))
    ws.send(JSON.stringify({ type: "replay_complete" }))
  })
  await page.goto("/")
  await expect(gwRow(page, "local:local-one")).toBeVisible()
  releasePeer()
  const remote = gwRow(page, "n-main:remote-one")
  await expect(remote).toBeVisible()
  await expect(gwSessions(page)).toContainText("Build server")
  await remote.click()
  await expect.poll(() => urls.length).toBe(1)
  expect(new URL(urls[0] ?? "").searchParams.get("node")).toBe("n-main")
})

test("restarting keeps the same operation ID after ambiguous failures", async ({ page }) => {
  await mockAPI(page)
  const ids: string[] = []
  await page.route("**/api/sessions/*/restart", (route) => {
    ids.push(gwString(route.request().postDataJSON(), "operation_id"))
    if (ids.length === 1) return route.abort("failed")
    if (ids.length === 2) return route.fulfill({ status: 502, json: { error: "Worker response unavailable" } })
    if (ids.length === 4) return route.fulfill({ status: 409, json: { error: "Restart rejected" } })
    return route.fulfill({ json: { restarted: true } })
  })
  await page.goto("/")
  const restart = async () => {
    await gwMore(page, "local:local-one").click()
    await page.getByRole("menuitem", { name: "Restart…" }).click()
    await page.getByRole("alertdialog").getByRole("button", { name: "Restart agent" }).click()
  }
  await restart()
  await expect(page.getByText(/Couldn't restart Claude on Gateway machine: (Failed to fetch|Load failed)/)).toBeVisible()
  await restart()
  await expect(page.getByText("Couldn't restart Claude on Gateway machine: Worker response unavailable")).toBeVisible()
  await restart()
  await expect.poll(() => ids.length).toBe(3)
  await expect(page.getByText("Agent restart requested", { exact: true })).toBeVisible()
  expect(ids[1]).toBe(ids[0])
  expect(ids[2]).toBe(ids[0])
  await restart()
  await expect(page.getByText("Couldn't restart Claude on Gateway machine: Restart rejected")).toBeVisible()
  expect(ids[3]).not.toBe(ids[2])
  await restart()
  await expect.poll(() => ids.length).toBe(5)
  expect(ids[4]).not.toBe(ids[3])
})

test("gateway settings stay separate from selected remote sessions", async ({ page }) => {
  const requests = await mockAPI(page)
  let enabled = true
  const settingsQueries: string[] = []
  await page.route("**/api/settings", (route) => {
    settingsQueries.push(new URL(route.request().url()).search)
    return route.fulfill({ json: { running: true, platform: "linux", sharing_enabled: enabled, allowed_peers: ["tag:server"] } })
  })
  await page.route("**/api/settings/sharing", (route) => {
    enabled = gwFlag(route.request().postDataJSON(), "enabled")
    return route.fulfill({ json: { sharing_enabled: enabled } })
  })
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await gwSettingsLink(page).click()
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name: "Sharing" }).click()
  await expect(page.getByText("tag:server", { exact: true })).toBeVisible()
  await page.getByRole("switch", { name: "Tailscale sharing" }).click()
  await expect(page.getByRole("switch", { name: "Tailscale sharing" })).not.toBeChecked()
  expect(settingsQueries.every((value) => !value.includes("node="))).toBeTruthy()

  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name: "Account" }).click()
  await page.getByRole("button", { name: "Stop all agents…", exact: true }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Stop all agents", exact: true }).click()
  await expect(page.getByText("Agents on the gateway machine have been stopped.")).toBeVisible()
  expect(requests.some((value) => value.path === "/api/node/stop-agents")).toBeTruthy()
})

test("a restarted process that exits immediately still replays its final output", async ({ page }) => {
  await page.setViewportSize(gwDesktopViewport)
  await mockAPI(page)
  let phase: "running" | "restarting" | "stopped" = "running"
  const sockets: WebSocketRoute[] = []
  const stopped = { ...session.state, running: false, restarting: false, exit_code: 1 }
  await page.route("**/api/sessions", (route) => {
    const state = phase === "restarting" ? { ...session.state, running: false, restarting: true } : phase === "stopped" ? stopped : session.state
    if (phase === "restarting") phase = "stopped"
    return route.fulfill({ json: [{ ...session, state }] })
  })
  await page.routeWebSocket(gwTerminalSocket, (ws) => {
    sockets.push(ws)
    const current = { ...(sockets.length === 1 ? session.state : stopped), shared_input: true, responder: 42 }
    ws.send(JSON.stringify({ type: "attached", attachment: 42, state: current }))
    if (sockets.length === 2) {
      ws.send(Buffer.from("REPLACEMENT FAILED: invalid configuration\r\n"))
      ws.send(JSON.stringify({ type: "replay_complete" }))
      ws.send(JSON.stringify({ type: "ended", state: stopped }))
    }
  })
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await expect.poll(() => sockets.length).toBe(1)
  phase = "restarting"
  gwSocket(sockets, 0).send(JSON.stringify({ type: "ended", state: { ...session.state, running: false, restarting: true } }))
  gwSocket(sockets, 0).close()
  await expect.poll(() => sockets.length).toBe(2)
  await expect.poll(() => gwBuffer(page)).toContain("REPLACEMENT FAILED: invalid configuration")
  await gwRefresh(page)
  await expect(gwRow(page, "local:local-one")).toContainText("Stopped")
  await expect(page.getByRole("button", { name: /Take control|Release control/ })).toHaveCount(0)
})

test("sessions layout renders at desktop, tablet and phone sizes in both themes", async ({ page }, testInfo) => {
  await mockAPI(page)
  await attachTerminal(page)
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  for (const [width, height, theme] of [
    [1440, 1000, "light"],
    [1440, 1000, "dark"],
    [1024, 768, "light"],
    [1024, 768, "dark"],
    [390, 844, "light"],
    [390, 844, "dark"],
  ] as const) {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height })
    await page.waitForTimeout(100)
    await page.screenshot({ path: `test-results/sessions-${width}-${theme}-${testInfo.project.name}.png` })
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
  }
})

test("reconnect preserves and dims the last frame until replay is rendered", async ({ page }) => {
  await page.setViewportSize(gwDesktopViewport)
  await mockAPI(page)
  const sockets: WebSocketRoute[] = []
  // The stage is aria-busy (and dimmed) until replay completes; the last frame stays up until the replayed bytes arrive.
  await page.routeWebSocket(gwTerminalSocket, (ws) => {
    sockets.push(ws)
    ws.send(JSON.stringify({ type: "attached", attachment: 40 + sockets.length, state: { ...session.state, shared_input: true, responder: 40 + sockets.length } }))
    if (sockets.length === 1) {
      ws.send(Buffer.from("LAST FRAME\r\n"))
      ws.send(JSON.stringify({ type: "replay_complete" }))
    }
  })
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await expect.poll(() => gwBuffer(page)).toContain("LAST FRAME")
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "false")

  gwSocket(sockets, 0).close()
  await expect.poll(() => sockets.length).toBe(2)
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "true")
  await expect.poll(() => gwBuffer(page)).toContain("LAST FRAME")
  await gwRefresh(page)
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "true")

  gwSocket(sockets, 1).send(Buffer.from("REPLAYED FRAME\r\n"))
  gwSocket(sockets, 1).send(Buffer.from("SECOND FRAME\r\n"))
  await expect.poll(() => gwBuffer(page)).toContain("SECOND FRAME")
  expect(await gwBuffer(page)).not.toContain("LAST FRAME")
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "true")
  gwSocket(sockets, 1).send(JSON.stringify({ type: "replay_complete" }))
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "false")
})

test("Sessions starts active and appearance settings persist across views and reload", async ({ page }) => {
  await page.setViewportSize(gwDesktopViewport)
  await mockAPI(page)
  await attachTerminal(page)
  await gwLibraryStubs(page)
  await page.goto("/")
  await gwRow(page, "local:local-one").click()
  await expect(page.getByRole("link", { name: "Sessions", exact: true })).toHaveAttribute("aria-current", "page")

  await gwSettingsLink(page).click()
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name: "Appearance" }).click()
  for (const theme of ["Dark", "Light"] as const) {
    await page.getByRole("group", { name: "Theme" }).getByRole("button", { name: theme, exact: true }).click()
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme.toLowerCase())
  }
  // The terminal takes the theme it is attached under, so returning to the session shows light.
  await page.getByRole("link", { name: "Sessions", exact: true }).click()
  await gwRow(page, "local:local-one").click()
  await expect.poll(() => page.evaluate(() => window.__agentpackTerminal?.options.theme?.background)).toBe("#ffffff")

  await page.getByRole("link", { name: "Library", exact: true }).click()
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light")
  await page.reload()
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light")

  await gwSettingsLink(page).click()
  await page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name: "Appearance" }).click()
  await page.getByRole("group", { name: "Theme" }).getByRole("button", { name: "System", exact: true }).click()
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", /./)
})

test("sessions and library show loaders until their requests finish", async ({ page }) => {
  await mockAPI(page)
  await gwLibraryStubs(page)
  let releaseSessions: () => void = () => {}
  const sessionsReady = new Promise<void>((resolve) => {
    releaseSessions = resolve
  })
  let releaseLibrary: () => void = () => {}
  const libraryReady = new Promise<void>((resolve) => {
    releaseLibrary = resolve
  })
  await page.route((url) => url.pathname === "/api/sessions", async (route) => {
    await sessionsReady
    await route.fulfill({ json: [session] })
  })
  await page.route((url) => url.pathname === "/api/library/packages", async (route) => {
    await libraryReady
    await route.fulfill({ json: [] })
  })
  await page.goto("/")
  // First load: skeleton rows stand in for the list, and "No sessions yet" is not claimed while loading.
  await expect(gwSessions(page).locator('[data-slot="skeleton"]').first()).toBeVisible()
  await expect(gwSessions(page)).not.toContainText("No sessions yet")
  releaseSessions()
  await expect(gwRow(page, "local:local-one")).toBeVisible()
  await expect(gwSessions(page).locator('[data-slot="skeleton"]')).toHaveCount(0)

  await page.getByRole("link", { name: "Library", exact: true }).click()
  await expect(page.getByRole("navigation", { name: "Library" }).locator('[data-slot="skeleton"]').first()).toBeVisible()
  releaseLibrary()
  await expect(page.getByRole("navigation", { name: "Library" }).locator('[data-slot="skeleton"]')).toHaveCount(0)
})

test("terminal renders a large replay promptly and accepts live output afterwards", async ({ page }) => {
  await mockAPI(page)
  let socket: WebSocketRoute | undefined
  const replay = Buffer.from("history line\r\n".repeat(75000) + "\x1b[2J\x1b[HREPLAY READY\r\n")
  await page.routeWebSocket(gwTerminalSocket, (ws) => {
    socket = ws
    ws.send(JSON.stringify({ type: "attached", attachment: 42, state: { ...session.state, shared_input: true, responder: 42 } }))
    for (let offset = 0; offset < replay.length; offset += 32768) ws.send(replay.subarray(offset, offset + 32768))
    ws.send(JSON.stringify({ type: "replay_complete" }))
  })
  await page.goto("/")
  const clickedAt = await page.evaluate(() => performance.now())
  await gwRow(page, "local:local-one").click()
  await expect(gwStage(page)).toHaveAttribute("aria-busy", "false")
  await expect.poll(() => gwBuffer(page)).toContain("REPLAY READY")
  const timing = await page.evaluate(() => {
    const mark = (name: string) => performance.getEntriesByName(`agentpack:terminal:${name}`).at(-1)?.startTime ?? Number.NaN
    return { firstFrame: mark("firstframe"), replayComplete: mark("replay_complete") }
  })
  expect(timing.firstFrame - clickedAt).toBeLessThan(1500)
  expect(timing.replayComplete - clickedAt).toBeLessThan(5000)
  socket?.send(Buffer.from("LIVE AFTER REPLAY\r\n"))
  await expect.poll(() => gwBuffer(page)).toContain("LIVE AFTER REPLAY")
})

test("failed session reads stay distinct from an empty session list", async ({ page }) => {
  await mockAPI(page)
  await page.route((url) => url.pathname === "/api/sessions", (route) => route.fulfill({ status: 502, body: "Session service unavailable" }))
  await page.goto("/")
  await expect(page.getByRole("alert")).toContainText("Couldn't load sessions from Gateway machine: Session service unavailable", { timeout: 15_000 })
  await expect(gwSessions(page)).not.toContainText("No sessions yet")
})
