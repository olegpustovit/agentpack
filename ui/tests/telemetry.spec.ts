import { spawn } from "node:child_process"
import path from "node:path"
import { expect, test } from "@playwright/test"

let origin = ""

test.beforeAll(async () => {
  const binary = process.env.AGENTPACK_TELEMETRY_TEST_BINARY
  if (!binary) return
  const child = spawn(binary, ["-test.run=^TestTelemetryBrowserFixture$"], {
    cwd: path.resolve(import.meta.dirname, "../../internal/network"),
    env: { ...process.env, AGENTPACK_TELEMETRY_FIXTURE: "1" },
  })
  origin = await new Promise<string>((resolve, reject) => {
    let output = ""
    child.stdout.on("data", (chunk) => {
      output += String(chunk)
      const match = output.match(/TELEMETRY (http:\/\/[^\s]+)/)
      if (match) resolve(match[1]!)
    })
    child.on("error", reject)
    child.on("exit", (code) => reject(new Error(`telemetry fixture exited ${code}: ${output}`)))
  })
})

test.afterAll(async ({ request }) => {
  if (origin) await request.post(`${origin}/__test_stop`)
})

test("a throwing component persists one browser error on pagehide with breadcrumbs and the last API trace", async ({ page, context }) => {
  test.skip(!origin, "make ui-e2e prepares the Go telemetry fixture")
  await context.addCookies([{ name: "agentpack_session", value: "telemetry-browser", url: origin, sameSite: "Strict" }])
  await page.goto(origin)
  await expect(page.getByText(/couldn't be shown/)).toBeVisible()
  await page.goto("about:blank")
  let events: { trace_id: string; attrs: { breadcrumbs: { kind: string; trace_id?: string }[] } }[] = []
  await expect.poll(async () => {
    events = await (await page.request.get(`${origin}/__test_events`)).json()
    return events.length
  }).toBe(1)
  const event = events[0]!
  expect(event.attrs.breadcrumbs.length).toBeGreaterThan(0)
  expect(event.trace_id).toBe(event.attrs.breadcrumbs.filter((crumb) => crumb.kind === "api").at(-1)?.trace_id)
  expect(events).toHaveLength(1)
})
