import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"
import { expect, test, type Locator, type Page, type Route } from "@playwright/test"
import { conversationImage, mockAPI, projectRow, session } from "./helpers"

const conversationFixture = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "../../testdata/acp/full.jsonl"),
  "utf8",
)
const composerFixture = [
  '{"seq":1,"ts":1,"kind":"agent_message_chunk","payload":{"text":{"text":"Ready for your prompt."}}}',
  '{"seq":2,"ts":2,"kind":"tool_call","payload":{"toolCallId":"edit-1","title":"Edit terminal_client.go","kind":"edit","status":"completed","content":[{"type":"diff","path":"internal/network/terminal_client.go","oldText":"old","newText":"new"}]}}',
].join("\n")

// The kit's look is enforced by CSS, which unit tests and role queries cannot see. A cascade mistake
// once made every link blue, every selected row invisible and every font size inert, with all tests green.
const style = (locator: Locator, property: string, pseudo?: string) =>
  locator.evaluate((element, [name, pseudoElement]) => getComputedStyle(element, pseudoElement).getPropertyValue(name!), [property, pseudo] as const)

const transparent = "rgba(0, 0, 0, 0)"

async function desktop(page: Page) {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockAPI(page)
}

test.describe("kit styling", () => {
  test.skip(({ isMobile }) => isMobile, "desktop chrome")

  test("base rules do not outrank utilities", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions")
    const row = page.locator('[data-row-key="local:local-one"]')
    await expect(row).toBeVisible()
    // Link text takes the foreground, not an accent link colour.
    expect(await style(row, "color")).toBe(await page.locator("body").evaluate((body) => getComputedStyle(body).color))
    // A real font-size utility: the toolbar subtitle is the 11 px token.
    await expect(page.getByText("2 sessions on 1 machine")).toBeVisible()
    expect(await style(page.getByText("2 sessions on 1 machine"), "font-size")).toBe("11px")
  })

  test("the selected row is painted and keeps contrast off focus", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions/local/local-one")
    const row = page.locator('[data-row-key="local:local-one"]')
    await expect(row).toHaveAttribute("data-selected", "true")
    const unfocused = await style(row, "background-color")
    expect(unfocused).not.toBe(transparent)
    // Gray selection keeps dark text; white belongs to the accent fill.
    expect(await style(row, "color")).toBe(await style(page.locator('[data-row-key="local:local-two"]'), "color"))
    await row.focus()
    await expect.poll(() => style(row, "background-color")).not.toBe(unfocused)
  })

  test("segmented controls show which segment is on", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions")
    const on = page.getByRole("button", { name: "Machine", exact: true })
    await expect(on).toHaveAttribute("aria-pressed", "true")
    expect(await style(on, "background-color")).not.toBe(await style(page.getByRole("button", { name: "Project", exact: true }), "background-color"))
  })

  test("popup selects show the label, not the raw value", async ({ page }) => {
    await desktop(page)
    await page.goto("/library")
    await expect(page.getByRole("combobox", { name: "Sort" })).toContainText("Type")
    expect((await page.getByRole("combobox", { name: "Sort" }).boundingBox())?.height).toBe(24)
  })

  test("meter colours follow what is left", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/limits", (route) =>
      route.fulfill({ json: { limits: [{ harness: "codex", state: "available", observed_at: new Date().toISOString(), windows: [
        { id: "a", label: "Plenty", remaining_percent: 70 },
        { id: "b", label: "Low", remaining_percent: 10 },
        { id: "c", label: "Out", remaining_percent: 2 },
      ] }] } }),
    )
    await page.goto("/limits")
    const fill = (name: string) => page.getByRole("meter", { name: `${name} remaining` }).evaluate((meter) => getComputedStyle(meter).getPropertyValue("--meter"))
    await expect(page.getByRole("meter", { name: "Low remaining" })).toBeVisible()
    expect(new Set([await fill("Plenty"), await fill("Low"), await fill("Out")]).size).toBe(3)
  })

  test("the topology diagram is drawn at its own size", async ({ page, browserName }) => {
    test.skip(browserName !== "chromium", "layout only")
    await desktop(page)
    await page.route("**/api/topology", (route) =>
      route.fulfill({ json: { self: { id: "g", name: "gw", os: "linux", relay: "" }, peers: [{ id: "p", name: "peer", os: "linux", state: "reachable", connection: "direct", relay: "", sessions: 0 }] } }),
    )
    await page.goto("/machines/topology")
    const box = await page.locator('[data-slot="topology-diagram"]').boundingBox()
    expect(box?.width).toBeGreaterThan(300)
  })

  test("the terminal background fills its pane in the active theme", async ({ page }) => {
    await desktop(page)
    await page.emulateMedia({ colorScheme: "light" })
    await page.goto("/sessions")
    await expect(page.getByRole("link", { name: "Settings" })).toBeVisible()
    expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--terminal-bg").trim())).toMatch(/^#f{3,6}$/)
  })

  test("conversation kit styling: bubble, tool row, agent text, permission card, composer glass", async ({ page }) => {
    await desktop(page)
    const text = `Ship it ${"long message ".repeat(60)}`
    const body = [
      { seq: 1, ts: 1, kind: "user_message_chunk", payload: { text: { text } } },
      { seq: 2, ts: 2, kind: "tool_call", payload: { toolCallId: "read-one", title: "Read worker.go", kind: "read", status: "completed" } },
      { seq: 3, ts: 3, kind: "agent_message_chunk", payload: { text: { text: "Plain agent reply" } } },
      { seq: 4, ts: 4, kind: "plan", payload: { entries: [{ priority: "high", status: "in_progress", content: "Publish the branch" }] } },
      { seq: 5, ts: 5, kind: "permission_request", payload: { requestId: "p1", tool: "Bash", summary: "Run tests", options: [{ id: "allow", label: "Allow once" }, { id: "deny", label: "Deny" }] } },
    ].map((entry) => `${JSON.stringify(entry)}\n`).join("")
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({ json: [{ ...session, transport: "acp" }] })
    })
    await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body, contentType: "application/x-ndjson" }))
    await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
    await page.goto("/sessions/local/local-one")
    const token = (property: string, value: string, read: string) =>
      page.evaluate(([property, value, read]) => {
        const probe = document.createElement("div")
        probe.style.setProperty(property, value)
        document.body.appendChild(probe)
        const color = getComputedStyle(probe).getPropertyValue(read)
        probe.remove()
        return color
      }, [property, value, read])

    // User bubbles use the neutral track token at the reference width.
    const bubble = page.locator('[data-slot="user-bubble"]')
    await expect(bubble).toContainText("Ship it")
    expect(await style(bubble, "background-color")).toBe(await token("background", "var(--track)", "background-color"))
    const width = (await bubble.boundingBox())?.width ?? 0
    expect(width).toBeGreaterThan(500)
    expect(width).toBeLessThanOrEqual(560)

    // Tool rows are unboxed and use tool-specific icons.
    const row = page.locator("[data-tool-row]")
    await expect(row).toContainText("Read worker.go")
    expect(await style(row, "background-color")).toBe(transparent)
    expect(await style(row, "border-top-width")).toBe("0px")
    await expect(row.locator("summary .lucide-book-open")).toHaveCount(1)

    // Agent messages use the body foreground, not the link accent.
    const block = page.locator('[data-slot="agent-message"]').first()
    await expect(block).toContainText("Plain agent reply")
    expect(await style(block, "color")).toBe(await style(page.locator("body"), "color"))

    // Permission cards use the warn border token and show focus.
    const card = page.locator('[data-slot="permission-card"]')
    await expect(card).toBeVisible()
    expect(await style(card, "border-top-color")).toBe(await token("border-top", "0.5px solid var(--warn)", "border-top-color"))
    await card.focus()
    expect(await style(card, "box-shadow")).not.toBe("none")

    // The composer dock is translucent chrome and carries the plan pill.
    const dock = page.locator(".backdrop-blur-md").last()
    await expect(dock).toBeVisible()
    expect(await style(dock, "backdrop-filter")).not.toBe("none")
    await expect(page.locator('[data-slot="plan-pill"]')).toContainText("Publish the branch")
  })
})

// Behaviour of the conversation pane: Chromium only, it cannot differ by engine.
test.describe("conversation pane", () => {
  test("notices and compactions render in the thread", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({ json: [{ ...session, transport: "acp" }] })
    })
    await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
    await page.route("**/api/sessions/*/blobs/**", (route) =>
      route.fulfill({ body: conversationImage, contentType: "image/png" }),
    )
    await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: conversationFixture, contentType: "application/x-ndjson" }))
    await page.goto("/sessions/local/local-one")
    const notice = page.locator('[data-slot="conversation-divider"]').first()
    await expect(notice).toContainText("Context compacted")
    await expect(notice).toContainText("Older messages were summarized.")
    const compaction = page.locator('[data-slot="conversation-compaction"]')
    await expect(compaction).toContainText("completed")
    await expect(compaction).toContainText("Compaction summary retained.")
  })


  test("composer slash menu and needs-you row label", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({
        json: [
          { ...session, transport: "acp", conversation_state: "needs_you" },
          { ...session, id: "local-two", name: "Another", conversation_state: "idle" },
        ],
      })
    })
    await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: composerFixture, contentType: "application/x-ndjson" }))
    await page.goto("/sessions")
    await expect(page.getByText("Needs you").first()).toBeVisible()
    await expect(page.getByText("Idle").first()).toBeVisible()
    await page.goto("/sessions/local/local-one")
    await page.getByRole("button", { name: "Commands" }).click()
    await expect(page.locator('[data-slot="composer-hints"]')).toBeVisible()
    const message = page.getByRole("textbox", { name: "Message" })
    await message.fill("/mod")
    await expect(page.locator('[data-slot="composer-hints"]')).toBeVisible()
    await message.fill("")
    await page.getByRole("button", { name: "Mention a file" }).click()
    await expect(page.locator('[data-slot="composer-hints"]')).toContainText("terminal_client.go")
    await expect(page.locator('[data-slot="composer-hints"]')).toBeVisible()
  })
})

test.describe("session list behaviour", () => {
  test.skip(({ isMobile }) => isMobile, "desktop keyboard")

  test("?q filters rows and survives reload", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions")
    await page.getByRole("searchbox", { name: "Filter sessions" }).fill("another")
    await expect(page).toHaveURL(/q=another/)
    await expect(page.locator('[data-row-key="local:local-one"]')).toHaveCount(0)
    await page.reload()
    await expect(page.locator('[data-row-key="local:local-two"]')).toBeVisible()
    await expect(page.locator('[data-row-key="local:local-one"]')).toHaveCount(0)
  })

  test("opening a session keeps the grouping", async ({ page }) => {
    await desktop(page)
    await page.goto("/sessions?group=project")
    await page.locator('[data-row-key="local:local-one"]').click()
    await expect(page).toHaveURL(/\/sessions\/local\/local-one\?group=project/)
  })

  test("arrows stay in the list while rows open; Enter moves to the terminal", async ({ page }) => {
    await desktop(page)
    await page.routeWebSocket("**/api/sessions/*/terminal*", (ws) => {
      ws.send(JSON.stringify({ type: "attached", attachment: 1, offset: 0, state: session.state }))
      ws.send(JSON.stringify({ type: "replay_complete" }))
    })
    await page.goto("/sessions")
    await page.locator('[data-row-key="local:local-two"]').focus()
    await page.keyboard.press("ArrowDown")
    await expect(page).toHaveURL(/\/sessions\/local\/local-one$/)
    await page.locator(".xterm-helper-textarea").waitFor({ state: "attached" })
    // Opening a row on the way must not steal focus, or the next arrow would be typed into the agent.
    await expect(page.locator('[data-row-key="local:local-one"]')).toBeFocused()
    await page.keyboard.press("Enter")
    await expect(page.locator(".xterm-helper-textarea")).toBeFocused()
  })

  test("clicking a row still moves focus to the terminal", async ({ page }) => {
    await desktop(page)
    await page.routeWebSocket("**/api/sessions/*/terminal*", (ws) => {
      ws.send(JSON.stringify({ type: "attached", attachment: 1, offset: 0, state: session.state }))
      ws.send(JSON.stringify({ type: "replay_complete" }))
    })
    await page.goto("/sessions")
    await page.locator('[data-row-key="local:local-one"]').click()
    await expect(page.locator(".xterm-helper-textarea")).toBeFocused()
  })

  test("Install package lists known packages with their state", async ({ page }) => {
    await desktop(page)
    const pin = { module: "github.com/acme/tools", commit: "a".repeat(40), content_hash: "", cache_key: "k", kind: "skill", name: "tools", url: "", direct: true }
    await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow("/work/my-project", [{ harness: "claude" }], { packages: [pin] }), projectRow("/work/another-project", [{ harness: "claude" }])] }))
    await page.route("**/api/library/packages", (route) => route.fulfill({ json: [{ ...pin, id: "k", owner: "acme", repo: "tools" }] }))
    const posted: { spec?: string; directory?: string }[] = []
    await page.route("**/api/packages", (route) => {
      posted.push(route.request().postDataJSON())
      return route.fulfill({ json: { installed: true } })
    })
    await page.goto("/sessions")
    const row = page.locator('[data-row-key="local:local-two"]')
    await row.hover()
    await page.getByRole("button", { name: "More actions" }).first().click()
    await page.getByRole("menuitem", { name: "Install package…" }).click()
    const known = page.getByRole("group", { name: "Known packages" })
    await expect(known.getByText("tools")).toBeVisible()
    await known.getByRole("checkbox").click()
    await page.getByRole("button", { name: "Install", exact: true }).click()
    await expect.poll(() => posted.length).toBe(1)
    expect(posted[0]).toMatchObject({ spec: `github.com/acme/tools@${"a".repeat(40)}`, directory: "/work/another-project" })
  })
})

test("a failing machine list still reports the gateway connection", async ({ page, isMobile }) => {
  test.skip(isMobile, "the status bar is desktop chrome")
  await desktop(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ status: 400, json: { error: "tailscale is not running" } }))
  await page.goto("/sessions")
  await expect(page.getByRole("contentinfo")).toContainText("Connected · 1 machine")
})

// Peers update on their own schedule, so the gateway routinely talks to older ones.
test.describe("a peer on an older version", () => {
  test.skip(({ isMobile }) => isMobile, "desktop")

  async function oldPeer(page: Page) {
    await desktop(page)
    await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "old", name: "main", online: true }] }))
    const unlessPeer = (json: unknown, peerJson: unknown, status = 200) => (route: Route) =>
      new URL(route.request().url()).searchParams.has("node") ? route.fulfill({ status, json: peerJson }) : route.fulfill({ json })
    // It ignores ?include=revisions and has no /api/projects.
    await page.route("**/api/library/agentpacks**", unlessPeer([], [{ id: "bare", name: "bare-pack", revisions: ["r1"], heads: ["r1"] }]))
    await page.route("**/api/projects**", unlessPeer([], "404 page not found", 404))
  }

  test("the Library lists its bare agentpacks instead of crashing", async ({ page }) => {
    await oldPeer(page)
    await page.goto("/library")
    await expect(page.getByRole("link", { name: /bare-pack/ })).toBeVisible()
  })

  test("Machines asks for an update, with no raw 404", async ({ page }) => {
    await oldPeer(page)
    await page.goto("/machines/old")
    await expect(page.getByText("Update Agentpack on main to see its projects.")).toBeVisible()
    await expect(page.getByText("404")).toHaveCount(0)
  })
})

test("a confirm alert keeps its stacked buttons inside it", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  await page.goto("/settings/account")
  await page.getByRole("button", { name: "Stop gateway…" }).click()
  const alert = await page.getByRole("alertdialog").boundingBox()
  const stop = await page.getByRole("alertdialog").getByRole("button", { name: "Stop gateway" }).boundingBox()
  const cancel = await page.getByRole("alertdialog").getByRole("button", { name: "Cancel" }).boundingBox()
  expect(stop!.x).toBeGreaterThanOrEqual(alert!.x)
  expect(stop!.x + stop!.width).toBeLessThanOrEqual(alert!.x + alert!.width)
  expect(cancel!.y).toBeGreaterThan(stop!.y)
})

test("a machine with no projects says so in New session", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [] }))
  await page.goto("/sessions?new=1")
  await expect(page.getByText("No projects yet")).toBeVisible()
  await expect(page.getByRole("combobox", { name: "Project" })).toHaveCount(0)
  await expect(page.getByRole("button", { name: "Start session" })).toBeDisabled()
})

test("a grid column can be dragged wider", async ({ page, isMobile, browserName }) => {
  test.skip(isMobile || browserName !== "chromium", "desktop pointer")
  await desktop(page)
  await page.goto("/limits")
  const header = page.getByRole("columnheader", { name: "Window" })
  const before = (await header.boundingBox())!
  const handle = (await page.getByRole("separator", { name: "Resize Window" }).boundingBox())!
  await page.mouse.move(handle.x + 1, handle.y + 5)
  await page.mouse.down()
  await page.mouse.move(handle.x + 81, handle.y + 5, { steps: 3 })
  await page.mouse.up()
  expect((await header.boundingBox())!.width).toBeCloseTo(before.width + 80, -1)
})

test("Update on an Installed in row moves only that project to the latest version", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  const pin = (commit: string) => ({ module: "github.com/acme/tools", commit: commit.repeat(40), content_hash: "", cache_key: commit, kind: "skill", name: "tools", url: "", direct: true })
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [projectRow("/work/a", [{ harness: "claude" }], { packages: [pin("a")] }), projectRow("/work/b", [{ harness: "claude" }], { packages: [pin("b")] })] }))
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: ["a", "b"].map((commit) => ({ ...pin(commit), id: commit, owner: "acme", repo: "tools" })) }))
  const posted: { spec: string; directory: string }[] = []
  await page.route("**/api/packages", (route) => {
    posted.push(route.request().postDataJSON())
    return route.fulfill({ json: { installed: true } })
  })
  await page.route("**/api/library/latest**", (route) => route.fulfill({ json: { commit: "b".repeat(40), current: ["b".repeat(40)], exact: true } }))
  await page.goto("/library/package/github.com/acme/tools")
  await page.getByRole("button", { name: "Update to bbbbbbb" }).click()
  await expect.poll(() => posted.length).toBe(1)
  expect(posted[0]).toMatchObject({ spec: `github.com/acme/tools@${"b".repeat(40)}`, directory: "/work/a" })
})

test("a long project path stays inside the New session sheet", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  const long = `/work/${"very-long-folder-name/".repeat(12)}project`
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [projectRow(long, [{ harness: "claude", mode: "" }])] }))
  await page.goto("/sessions?new=1")
  const sheet = (await page.getByRole("dialog").boundingBox())!
  const start = (await page.getByRole("button", { name: "Start session" }).boundingBox())!
  const project = (await page.getByRole("combobox", { name: "Project" }).boundingBox())!
  expect(start.x + start.width).toBeLessThanOrEqual(sheet.x + sheet.width)
  expect(project.x + project.width).toBeLessThanOrEqual(sheet.x + sheet.width)
  // A CLI launch records an empty mode; the field still names the real one.
  await expect(page.getByLabel("Mode")).toHaveValue("default")
})

test("a stop in progress shows on its row until it finishes", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  let finish = () => {}
  const held = new Promise<void>((resolve) => (finish = resolve))
  await page.route("**/api/sessions/local-one/stop", async (route) => {
    await held
    return route.fulfill({ json: { stopped: true } })
  })
  await page.goto("/sessions")
  const wrapper = page.locator('[data-row-key="local:local-one"]').locator("..")
  await wrapper.hover()
  await wrapper.getByRole("button", { name: "More actions" }).click()
  await page.getByRole("menuitem", { name: "Stop…" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Stop agent" }).click()
  await expect(wrapper).toContainText("Stopping…")
  finish()
  await expect(wrapper).not.toContainText("Stopping…")
})
