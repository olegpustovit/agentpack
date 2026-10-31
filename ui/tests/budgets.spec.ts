import { expect, test, type Page, type Request } from "@playwright/test"
import { fleetSessions, mockAPI } from "./helpers"
import { cell, openRoute, screen, serve, stubWorker } from "./terminal-stub"

const viewports = [
  { width: 1440, height: 900 },
  { width: 800, height: 400 },
]

// Replaces EventSource with a stream that opens once and never errors or delivers messages, so the app
// runs with events connected. route.fulfill cannot hold an SSE response open.
function openEventStream() {
  class OpenStream {
    onopen: ((event: Event) => void) | null = null
    onerror: ((event: Event) => void) | null = null
    onmessage: ((event: MessageEvent<string>) => void) | null = null
    readyState = 1
    constructor() {
      setTimeout(() => this.onopen?.(new Event("open")), 0)
    }
    close() {
      this.readyState = 2
    }
  }
  Object.defineProperty(window, "EventSource", { value: OpenStream, configurable: true })
}

// The audit fixture (3 machines, 4 projects, 3 harnesses per project) from the shared helper.
function mockFleet(page: Page) {
  return mockAPI(page, { fleet: true })
}

// Records every /api request path made while it is listening.
function countApi(page: Page) {
  const paths: string[] = []
  const onRequest = (request: Request) => {
    const { pathname } = new URL(request.url())
    if (pathname.startsWith("/api/")) paths.push(pathname)
  }
  page.on("request", onRequest)
  return { paths, stop: () => page.off("request", onRequest) }
}

const skeletons = (page: Page) => page.locator('[data-slot="skeleton"]')
const sessionRows = (page: Page) => page.locator('nav[aria-label="Sessions"] [data-source-row]')
const libraryEmpty = (page: Page) => page.getByText("Nothing in the library yet")

test("Idle Sessions makes at most 6 requests per 60 s with events connected", async ({ page }) => {
  await page.clock.install()
  await page.addInitScript(openEventStream)
  await mockFleet(page)
  await page.goto("/sessions")
  await expect(sessionRows(page)).toHaveCount(36)
  // Each machine's list loads after its node is known; the window starts once all three have settled.
  await page.waitForLoadState("networkidle")

  const seen = countApi(page)
  await page.clock.runFor(60_000)
  seen.stop()
  expect(seen.paths.length).toBeLessThanOrEqual(6)
})

test("Idle Library makes at most 3 requests per 30 s with events connected", async ({ page }) => {
  await page.clock.install()
  await page.addInitScript(openEventStream)
  await mockFleet(page)
  await page.goto("/library")
  await expect(libraryEmpty(page)).toBeVisible()
  await page.waitForLoadState("networkidle")

  const seen = countApi(page)
  await page.clock.runFor(30_000)
  seen.stop()
  expect(seen.paths.length).toBeLessThanOrEqual(3)
})

test("the Sessions sidebar has 0 DOM mutations per 60 s of unchanged data", async ({ page }) => {
  await page.clock.install()
  await page.addInitScript(openEventStream)
  await mockFleet(page)
  await page.goto("/sessions")
  await expect(sessionRows(page)).toHaveCount(36)
  await page.waitForLoadState("networkidle")

  const polls = countApi(page)
  await page.evaluate(() => {
    const nav = document.querySelector('nav[aria-label="Sessions"]')
    if (!nav) throw new Error("sessions sidebar not found")
    const counter = window as Window & { churn?: number }
    counter.churn = 0
    new MutationObserver((records) => {
      counter.churn = (counter.churn ?? 0) + records.length
    }).observe(nav, { subtree: true, childList: true, attributes: true, characterData: true })
  })

  await page.clock.runFor(60_000)
  polls.stop()
  // The sidebar must have polled during the window, or the zero count proves nothing.
  expect(polls.paths.filter((path) => path === "/api/sessions").length).toBeGreaterThanOrEqual(1)
  expect(await page.evaluate(() => (window as Window & { churn?: number }).churn)).toBe(0)
})

// Returning inside staleTime re-reads nothing: the status guard and the Library queries are still fresh.
test("returning to Library within staleTime sends no requests and shows no loader", async ({ page }) => {
  await page.addInitScript(openEventStream)
  await mockFleet(page)
  await page.goto("/library")
  await expect(libraryEmpty(page)).toBeVisible()
  await page.getByRole("link", { name: "Sessions", exact: true }).click()
  await expect(sessionRows(page)).toHaveCount(36)
  await page.waitForLoadState("networkidle")

  const seen = countApi(page)
  await page.getByRole("link", { name: "Library", exact: true }).click()
  await expect(libraryEmpty(page)).toBeVisible()
  seen.stop()
  expect(seen.paths).toEqual([])
  await expect(skeletons(page)).toHaveCount(0)
})

// Counts the per-machine library queries (packages, agentpacks, projects on 3 machines). The drafts list loads with the shell.
test("first Library open makes at most 9 requests", async ({ page }) => {
  await page.addInitScript(openEventStream)
  await mockFleet(page)
  await page.goto("/sessions")
  await expect(sessionRows(page)).toHaveCount(36)
  await page.waitForLoadState("networkidle")

  const seen = countApi(page)
  await page.getByRole("link", { name: "Library", exact: true }).click()
  await expect(libraryEmpty(page)).toBeVisible()
  await page.waitForLoadState("networkidle")
  seen.stop()
  expect(seen.paths.length).toBeLessThanOrEqual(9)
})

test("the fleet's install rows total at most 12 across machines", async ({ page }) => {
  await mockFleet(page)
  let rows = 0
  for (const machine of ["local", "peer-1", "peer-2"]) {
    await page.goto(`/machines/${machine}`)
    const grid = page.locator('[aria-label="Projects and pinned packages"] tbody tr')
    await expect(grid.first()).toBeVisible()
    rows += await grid.count()
  }
  expect(rows).toBeLessThanOrEqual(12)
})

test("New Session offers at most 4 folders on each machine", async ({ page }) => {
  await mockFleet(page)
  await page.goto("/sessions?new=1")
  const machine = page.getByRole("combobox", { name: "Machine" })
  const project = page.getByRole("combobox", { name: "Project" })
  for (const name of ["Gateway machine", "Build server", "oracle-vm"]) {
    if (name !== "Gateway machine") {
      await machine.click()
      await page.getByRole("option", { name, exact: true }).click()
    }
    await project.click()
    await expect(page.getByRole("option")).toHaveCount(4)
    await page.keyboard.press("Escape")
  }
})

// The old UI discarded any answer slower than its 5 s poll. Hold one past several polls, then release it.
test("a peer that answers after several polls still shows its sessions", async ({ page }) => {
  await page.clock.install()
  await mockFleet(page)
  let answer = () => {}
  const held = new Promise<void>((resolve) => (answer = resolve))
  // Registered after mockFleet, so it takes precedence for the peer's session list only.
  await page.route(/\/api\/sessions(\?.*)?$/, async (route) => {
    const url = new URL(route.request().url())
    if (route.request().method() !== "GET" || url.searchParams.get("node") !== "peer-1") return route.fallback()
    await held
    return route.fulfill({ json: fleetSessions("peer-1") })
  })
  const asked = page.waitForRequest((request) => request.url().includes("/api/sessions") && new URL(request.url()).searchParams.get("node") === "peer-1")
  await page.goto("/sessions")
  await asked
  await page.clock.runFor(6_000)
  answer()
  await page.locator('a[href="/sessions/peer-1/peer-1-alpha-claude"]').waitFor({ state: "visible" })
})

for (const viewport of viewports) {
  const size = `${viewport.width}×${viewport.height}`

  test(`a click at cell 61,21 (1-based) is reported as 61;21 at ${size}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await mockFleet(page)
    const worker = await stubWorker(page)
    worker.behave((conn) => serve(conn, { body: screen("A", { mouseModes: true }) }))
    await openRoute(page, "/sessions/local/local-alpha-claude")
    const point = await cell(page, 60, 20)
    await page.mouse.click(point.x, point.y)
    await expect.poll(() => worker.conns[0]?.sent).toContain("\x1b[<0;61;21M")
  })
}

test("a terminal notice leaves the terminal content height unchanged", async ({ page }) => {
  await mockFleet(page)
  const worker = await stubWorker(page)
  worker.behave((conn) => serve(conn, { body: screen("A") }))
  await openRoute(page, "/sessions/local/local-alpha-claude")
  const content = page.locator(".xterm-screen")
  const before = (await content.boundingBox())?.height

  // A second attach with an offset raises the "Earlier output was truncated" notice.
  const conn = worker.conns[0]
  if (!conn) throw new Error("terminal socket was not opened")
  serve(conn, { offset: 4, complete: false })
  await expect(page.getByRole("status")).toContainText("Earlier output was truncated")
  expect((await content.boundingBox())?.height).toBe(before)
})
