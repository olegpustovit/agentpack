import { expect, test, type Page } from "@playwright/test"
import { busyStage, type Listed, openFromList, openRoute, rowText, screen, serve, session, shared, statusBar, stubApi, stubWorker } from "./terminal-stub"

const A = screen("A")
const B = screen("B")
const names = ["one", "two", "three", "four", "five"]
let sessions: Listed[] = []
// Set by a test to make s1 report a restart on every list request.
let s1Restarting = false
let listRequests = 0

test.beforeEach(async ({ page }) => {
  sessions = names.map((name, index) => session(`s${index + 1}`, name))
  listRequests = 0
  s1Restarting = false
  await stubApi(page, () => {
    listRequests++
    return sessions.map((item) => (s1Restarting && item.id === "s1" ? { ...item, state: { ...shared, running: false, restarting: true } } : item))
  })
})

// Lets the browser handle queued events while the fake clock is paused.
async function settle(page: Page, ms = 150) {
  await page.waitForTimeout(ms)
}

function ended(conn: { ws: { send(message: string): void }; index: number }, restarting: boolean) {
  conn.ws.send(JSON.stringify({ type: "ended", attachment: conn.index, state: { ...shared, running: !restarting, restarting } }))
}

test("returning to a recent session opens no socket and keeps the screen", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  await page.evaluate(() => {
    window.__first = window.__agentpackTerminal
    window.__writes = 0
    const term = window.__agentpackTerminal as unknown as { write(data: unknown, callback?: unknown): void }
    const original = term.write.bind(term)
    term.write = (data, callback) => {
      window.__writes = (window.__writes ?? 0) + 1
      original(data, callback)
    }
  })
  await openFromList(page, "two")
  await expect.poll(() => worker.conns.length).toBe(2)
  // Output for the hidden session keeps arriving and lands in its buffer.
  worker.conns[0]?.ws.send(Buffer.from("\x1b[1;1HZZZZ"))
  await expect.poll(() => page.evaluate(() => window.__first?.buffer.active.getLine(0)?.translateToString(true) ?? "")).toContain("ZZZZ")
  const writesBeforeReturn = await page.evaluate(() => window.__writes ?? 0)

  await openFromList(page, "one")
  await expect.poll(() => page.evaluate(() => window.__agentpackTerminal === window.__first)).toBe(true)
  // First frame after the switch: the content is there and nothing was written.
  const firstFrame = await page.evaluate(() => new Promise<string>((resolve) => requestAnimationFrame(() => resolve(window.__first?.buffer.active.getLine(0)?.translateToString(true) ?? ""))))
  expect(firstFrame).toContain("ZZZZ")
  expect(firstFrame).toContain("AAAA")
  expect(worker.conns).toHaveLength(2)
  expect(await page.evaluate(() => window.__writes ?? 0)).toBe(writesBeforeReturn)
})

test("a dropped socket reconnects with since set to the bytes already received", async ({ page }) => {
  const worker = await stubWorker(page)
  worker.behave((conn) => (conn.index === 1 ? serve(conn, { body: A }) : serve(conn, { offset: Number(conn.since) })))
  await openRoute(page, "/sessions/local/s1")
  worker.conns[0]?.ws.close()
  await expect.poll(() => worker.conns.length).toBe(2)
  expect(worker.conns[1]?.since).toBe(String(A.length))
  await expect(statusBar(page)).toContainText("Shared input")
  // The worker resumed where the browser was, so the screen was not reset.
  await expect.poll(() => rowText(page, 0)).toContain("AAAA")
})

test("a stale since is answered with a different offset and the screen resets", async ({ page }) => {
  const worker = await stubWorker(page)
  const oneRow = Buffer.from("\x1b[1;1H" + "B".repeat(100))
  worker.behave((conn) => (conn.index === 1 ? serve(conn, { body: A }) : serve(conn, { offset: 0, body: oneRow })))
  await openRoute(page, "/sessions/local/s1")
  worker.conns[0]?.ws.close()
  await expect.poll(() => worker.conns.length).toBe(2)
  expect(worker.conns[1]?.since).toBe(String(A.length))
  await expect.poll(() => rowText(page, 0)).toContain("BBBB")
  // Reset cleared the rest of the old screen; without it rows 2 and below would still show "A".
  expect(await rowText(page, 1)).toBe("")
})

test("repeated drops back off 0.5 s, 1 s, 2 s, and an attach resets the backoff", async ({ page }) => {
  await page.clock.install()
  const worker = await stubWorker(page)
  worker.behave((conn) => {
    if (conn.index === 1) serve(conn, { body: A })
    else if (conn.index === 4) serve(conn, { offset: Number(conn.since) })
    else conn.ws.close()
  })
  await openRoute(page, "/sessions/local/s1")
  // Paused only once the replay has drained: xterm's write callbacks need timers.
  await page.clock.pauseAt(Date.now() + 1000)
  const drop = async (count: number, next: number) => {
    await expect.poll(() => worker.conns.length).toBe(count)
    await expect.poll(() => worker.conns[count - 1]?.closed).toBe(true)
    await settle(page)
    await page.clock.runFor(next - 1)
    await settle(page)
    expect(worker.conns).toHaveLength(count)
    await page.clock.runFor(1)
  }
  worker.conns[0]?.ws.close()
  await expect(busyStage(page)).toHaveCount(1)
  await settle(page)
  await page.clock.runFor(499)
  await settle(page)
  expect(worker.conns).toHaveLength(1)
  await page.clock.runFor(1)
  await expect.poll(() => worker.conns.length).toBe(2)
  await drop(2, 1000)
  await expect.poll(() => worker.conns.length).toBe(3)
  await drop(3, 2000)
  await expect.poll(() => worker.conns.length).toBe(4)
  await expect(statusBar(page)).toContainText("Shared input")
  worker.conns[3]?.ws.close()
  await expect(busyStage(page)).toHaveCount(1)
  await settle(page)
  await page.clock.runFor(499)
  await settle(page)
  expect(worker.conns).toHaveLength(4)
  await page.clock.runFor(1)
  await expect.poll(() => worker.conns.length).toBe(5)
})

test("a socket whose replay never completes is closed after 30 s", async ({ page }) => {
  // Real time: the deadline starts at connect, which a fake clock cannot pin down from outside.
  test.setTimeout(60_000)
  const worker = await stubWorker(page)
  worker.behave((conn) => serve(conn, { complete: false }))
  await page.goto("/sessions/local/s1")
  await expect(busyStage(page)).toHaveCount(1)
  await page.waitForTimeout(29_000)
  expect(worker.conns[0]?.closed).toBe(false)
  await expect.poll(() => worker.conns[0]?.closed, { timeout: 5_000 }).toBe(true)
})

test("unacknowledged output past 16 MiB closes the socket and says so", async ({ page }) => {
  const worker = await stubWorker(page)
  worker.behave((conn) => serve(conn, {}))
  await openRoute(page, "/sessions/local/s1")
  // Writes that never complete keep their bytes counted as pending.
  await page.evaluate(() => {
    window.__arrivals = 0
    const term = window.__agentpackTerminal as unknown as { write(data: unknown): void }
    term.write = () => {
      window.__arrivals = (window.__arrivals ?? 0) + 1
    }
  })
  const chunk = Buffer.alloc(4 * 1024 * 1024, "x")
  for (let index = 0; index < 4; index++) worker.conns[0]?.ws.send(chunk)
  await expect.poll(() => page.evaluate(() => window.__arrivals ?? 0)).toBe(4)
  // Exactly 16 MiB is allowed; one more byte is not.
  await expect(page.getByRole("status").filter({ hasText: "Output exceeded" })).toHaveCount(0)
  worker.conns[0]?.ws.send(Buffer.from("x"))
  await expect(page.getByRole("status").filter({ hasText: "Output exceeded" })).toBeVisible()
  await expect.poll(() => worker.conns[0]?.closed).toBe(true)
  expect(await page.evaluate(() => window.__arrivals ?? 0)).toBe(4)
})

test("a restarting agent is polled until it is listed, then reattaches as a fresh process", async ({ page }) => {
  const worker = await stubWorker(page)
  worker.behave((conn) => (conn.index === 1 ? serve(conn, { body: A }) : serve(conn, { body: B })))
  await openRoute(page, "/sessions/local/s1")
  const before = listRequests
  s1Restarting = true
  ended(worker.conns[0]!, true)
  await expect(page.getByRole("status").filter({ hasText: "Agent restarting" })).toBeVisible()
  await expect.poll(() => listRequests).toBeGreaterThanOrEqual(before + 3)
  s1Restarting = false
  await expect.poll(() => worker.conns.length).toBe(2)
  expect(worker.conns[1]?.since).toBeNull()
  await expect(page.getByRole("status").filter({ hasText: "Agent restarted. This is a new process" })).toBeVisible()
  await expect(statusBar(page)).toContainText("Shared input")
  // The new terminal shows only the new process's output.
  await expect.poll(() => rowText(page, 0)).toContain("BBBB")
  expect(await rowText(page, 0)).not.toContain("AAAA")
})

test("disposing a session stops its restart polling", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  s1Restarting = true
  ended(worker.conns[0]!, true)
  await expect.poll(() => listRequests).toBeGreaterThan(3)
  for (const [index, name] of ["two", "three", "four", "five"].entries()) {
    await openFromList(page, name)
    await expect.poll(() => worker.conns.length).toBe(index + 2)
  }
  // Five sessions: the restarting one was the least recently used, so it is disposed.
  await expect.poll(() => worker.conns[0]?.closed).toBe(true)
  const after = listRequests
  await settle(page, 600)
  expect(listRequests).toBeLessThanOrEqual(after + 1)
})

test("a restart that never completes gives up after 90 s", async ({ page }) => {
  await page.clock.install()
  const worker = await stubWorker(page)
  worker.behave((conn) => serve(conn, { body: A }))
  await openRoute(page, "/sessions/local/s1")
  await page.clock.pauseAt(Date.now() + 1000)
  s1Restarting = true
  ended(worker.conns[0]!, true)
  const giveUp = page.getByRole("status").filter({ hasText: "The agent has not become available" })
  // Each poll is a real round trip, so the fake clock advances in steps between them.
  for (let step = 0; step < 300 && (await giveUp.count()) === 0; step++) {
    await page.clock.runFor(1000)
    await settle(page, 30)
  }
  await expect(giveUp).toBeVisible()
  const stopped = listRequests
  await settle(page, 600)
  expect(listRequests).toBeLessThanOrEqual(stopped + 1)
})

test("a fifth session evicts the least recently used one and leaves the rest open", async ({ page }) => {
  const worker = await stubWorker(page)
  await openRoute(page, "/sessions/local/s1")
  for (const [index, name] of ["two", "three", "four", "five"].entries()) {
    await openFromList(page, name)
    await expect.poll(() => worker.conns.length).toBe(index + 2)
  }
  await expect.poll(() => worker.conns[0]?.closed).toBe(true)
  for (const conn of worker.conns.slice(1)) expect(conn.closed).toBe(false)
  expect(worker.conns).toHaveLength(5)
})
