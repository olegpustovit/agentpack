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
    await page.goto("/sessions?group=machine")
    const on = page.getByRole("button", { name: "Machine", exact: true })
    await expect(on).toHaveAttribute("aria-pressed", "true")
    expect(await style(on, "background-color")).not.toBe(await style(page.getByRole("button", { name: "Project", exact: true }), "background-color"))
  })

  test("popup selects show the label, not the raw value", async ({ page }) => {
    await desktop(page)
    await page.goto("/library")
    await expect(page.getByRole("combobox", { name: "Sort" })).toContainText("Type")
    expect((await page.getByRole("combobox", { name: "Sort" }).boundingBox())?.height).toBe(26)
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
    await page.goto("/usage/limits")
    const fill = (name: string) => page.getByRole("meter", { name: `${name} remaining` }).evaluate((meter) => getComputedStyle(meter).getPropertyValue("--meter"))
    await expect(page.getByRole("meter", { name: "Low remaining" })).toBeVisible()
    expect(new Set([await fill("Plenty"), await fill("Low"), await fill("Out")]).size).toBe(3)
  })

  test("the usage hero: one large number, a dashed average and a fixed colour per agent", async ({ page, browserName }) => {
    test.skip(browserName !== "chromium", "layout only")
    await desktop(page)
    const hour = Math.floor(Date.now() / 3_600_000) * 3600
    const row = (harness: string, output_tokens: number) => ({ hour, node_id: "gateway", harness, folder_id: "f", model: "", turns: 1, sessions: 1, input_tokens: 0, output_tokens, cache_read_tokens: 0, cache_write_tokens: 0, cost_micros: 0, agent_ms: 0 })
    await page.route("**/api/usage?**", (route) => route.fulfill({ json: { hourly: [row("claude", 9000), row("codex", 3000)], sessions: [], samples: [] } }))
    await page.goto("/usage")
    const hero = page.locator('[data-slot="usage-hero"]')
    await expect(hero).toContainText("12K")
    // Exact: on the first day of a week the daily average reads 12K too.
    expect(await style(hero.getByText("12K", { exact: true }), "font-size")).toBe("28px")
    expect(await style(page.locator('[data-slot="chart-average"]'), "stroke-dasharray")).not.toBe("none")
    // The legend swatch and the bar share the agent's token, so Claude reads the same colour everywhere.
    const swatch = page.getByRole("list", { name: "Legend" }).getByRole("listitem").first().locator("span[aria-hidden]")
    const claude = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--series-1").trim())
    expect(claude).not.toBe("")
    expect(await style(swatch, "background-color")).toBe(await page.evaluate((value) => {
      const probe = document.createElement("span")
      probe.style.backgroundColor = value
      document.body.append(probe)
      return getComputedStyle(probe).backgroundColor
    }, claude))
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

  test("waiting strip is opaque, so the thread never shows through it", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({ json: [{ ...session, transport: "acp", conversation_state: "idle", waiting: [{ kind: "shell" }, { kind: "monitor" }] }] })
    })
    await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: composerFixture, contentType: "application/x-ndjson" }))
    await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
    await page.goto("/sessions/local/local-one")
    const strip = page.locator('[data-slot="waiting-strip"]')
    await expect(strip).toContainText("2 background tasks")
    // Opaque base colour (no alpha channel); the accent tint is layered on top as an image.
    expect(await style(strip, "background-color")).not.toMatch(/rgba|transparent|\/\s*0?\.\d/)
    expect(await style(strip, "background-image")).toContain("linear-gradient")
  })

  test("conversation kit styling: bubble, tool row, agent text, permission card, composer glass", async ({ page }) => {
    await desktop(page)
    const text = `Ship it ${"long message ".repeat(60)}`
    const body = [
      { seq: 1, ts: 1, kind: "user_message_chunk", payload: { text: { text } } },
      { seq: 2, ts: 2, kind: "tool_call", payload: { toolCallId: "read-one", title: "Read worker.go", kind: "read", status: "completed" } },
      { seq: 3, ts: 3, kind: "agent_message_chunk", payload: { text: { text: "Plain agent reply\n\nSecond paragraph\n\n- first point\n- second point" } } },
      { seq: 4, ts: 4, kind: "plan", payload: { entries: [{ priority: "high", status: "in_progress", content: "Publish the branch" }] } },
      { seq: 5, ts: 5, kind: "permission_request", payload: { requestId: "p1", tool: "Bash", summary: "Run tests", options: [{ id: "allow", label: "Allow once" }, { id: "deny", label: "Deny" }] } },
      { seq: 6, ts: 6, kind: "prompt", payload: { from: { session: "local-two", name: "Reviewer", task: "t_1", kind: "reply", state: "completed" }, text: { text: "[agentpack reply]\n---\nLooks **fine**." } } },
    ].map((entry) => `${JSON.stringify(entry)}\n`).join("")
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({ json: [{ ...session, transport: "acp" }] })
    })
    await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body, contentType: "application/x-ndjson" }))
    await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
    await page.goto("/sessions/local/local-one")
    const token = (property: string, value: string, read: string) =>
      page.evaluate(({ property, value, read }) => {
        const probe = document.createElement("div")
        probe.style.setProperty(property, value)
        document.body.appendChild(probe)
        const color = getComputedStyle(probe).getPropertyValue(read)
        probe.remove()
        return color
      }, { property, value, read })

    // User messages are full-width cards on the thread's left edge, in the neutral track token.
    const bubble = page.locator('[data-slot="user-bubble"]')
    await expect(bubble).toContainText("Ship it")
    expect(await style(bubble, "background-color")).toBe(await token("background", "var(--track)", "background-color"))
    const box = await bubble.boundingBox()
    const agentBox = await page.locator('[data-slot="agent-message"]').first().boundingBox()
    expect(box?.width ?? 0).toBeGreaterThan(560)
    expect(Math.abs((box?.x ?? 0) - (agentBox?.x ?? -100))).toBeLessThan(2)

    // Another agent's answer is an outlined card, not a filled bubble, with its markdown rendered.
    const reply = page.locator('[data-slot="reply-card"]')
    await expect(reply).toContainText("Reply from Reviewer · completed")
    await expect(reply.locator("strong")).toHaveText("fine")
    await expect(reply.getByRole("button", { name: "Open Reviewer" })).toBeVisible()
    expect(await style(reply, "background-color")).toBe(transparent)
    expect(await style(reply, "border-top-color")).toBe(await token("border-top", "0.5px solid var(--sep)", "border-top-color"))

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
    // Paragraphs are spaced and lists keep their bullets despite preflight.
    expect(await style(block.locator(".md > div").nth(1), "margin-top")).not.toBe("0px")
    expect(await style(block.locator("ul"), "list-style-type")).toBe("disc")

    // Permission cards use the warn border token and show focus.
    const card = page.locator('[data-slot="permission-card"]')
    await expect(card).toBeVisible()
    expect(await style(card, "border-top-color")).toBe(await token("border-top", "0.5px solid var(--warn)", "border-top-color"))
    await card.focus()
    expect(await style(card, "box-shadow")).not.toBe("none")

    // The plan pill above the composer is translucent chrome.
    const dock = page.locator('[data-slot="plan-pill"]')
    await expect(dock).toBeVisible()
    expect(await style(dock, "backdrop-filter")).not.toBe("none")
    await expect(page.locator('[data-slot="plan-pill"]')).toContainText("Publish the branch")
  })
})

// Behaviour of the conversation pane: Chromium only, it cannot differ by engine.
test.describe("conversation pane", () => {
  test("bypass mode is red, menus use NSMenu metrics, and the composer is focused close under the thread", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({ json: [{ ...session, transport: "acp" }] })
    })
    await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: composerFixture, contentType: "application/x-ndjson" }))
    await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [
      { type: "select", id: "mode", name: "Mode", currentValue: "bypassPermissions", options: [{ value: "default", name: "Manual" }, { value: "plan", name: "Plan" }, { value: "bypassPermissions", name: "Bypass permissions" }] },
    ] }))
    await page.goto("/sessions/local/local-one")
    await expect(page.getByRole("textbox", { name: "Message" })).toBeFocused()

    const trigger = page.getByRole("combobox", { name: "Permissions" })
    await expect(trigger).toHaveAttribute("data-danger", "true")
    expect(await style(trigger, "background-color")).not.toBe(transparent)

    // The thread reserves the composer's height plus a 36 px gap, not a fixed 256 px.
    const composer = page.locator('[aria-label="Conversation"] ~ div').filter({ has: page.getByRole("textbox", { name: "Message" }) }).locator("> div").first()
    const reserved = await page.locator('[aria-label="Conversation"]').evaluate((element) => parseFloat(getComputedStyle(element).paddingBottom))
    expect(reserved - (await composer.boundingBox())!.height).toBe(36)

    await trigger.click()
    const bypass = page.getByRole("option", { name: "Bypass permissions" })
    await expect(bypass).toBeVisible()
    expect(await style(bypass, "height")).toBe("26px")
    expect(await style(page.locator('[data-slot="select-content"]'), "border-top-left-radius")).toBe("12px")
    // Dangerous modes are their own group.
    await expect(page.locator('[data-slot="select-separator"]')).toHaveCount(1)
  })

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
    const compaction = page.locator('[data-slot="conversation-compaction"]')
    await expect(compaction).toHaveCount(1)
    await expect(compaction).toContainText("Context compacted")
    await expect(compaction).not.toContainText("completed")
    await expect(compaction).toContainText("Compaction summary retained.")
  })

  test("reviewing a changed file widens the inspector and sends line comments as one prompt", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({ json: [{ ...session, transport: "acp" }] })
    })
    await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: composerFixture, contentType: "application/x-ndjson" }))
    const prompts: unknown[] = []
    await page.route("**/api/sessions/*/conversation/prompt", (route) => {
      prompts.push(route.request().postDataJSON())
      return route.fulfill({ json: {} })
    })
    await page.goto("/sessions/local/local-one")
    const inspector = page.locator('[data-slot="conversation-inspector"]')
    expect(await style(inspector, "width")).toBe("272px")
    await inspector.getByRole("button", { name: /terminal_client\.go/ }).click()
    expect(await style(inspector, "width")).toBe("480px")
    const line = inspector.locator('[data-slot="inspector-diff"] .group').last()
    const add = line.locator('[data-slot="diff-comment-add"]')
    expect(await style(add, "opacity")).toBe("0")
    await line.hover()
    await expect.poll(() => style(add, "opacity")).toBe("1")
    await add.click()
    await inspector.getByRole("textbox", { name: "Comment on internal/network/terminal_client.go:1" }).fill("Clamp the offset.")
    await inspector.getByRole("button", { name: "Add comment" }).click()
    await inspector.getByRole("button", { name: "Send 1 comment to Claude" }).click()
    await expect.poll(() => prompts).toEqual([{ text: "internal/network/terminal_client.go:1: Clamp the offset." }])
    await expect(inspector.getByRole("button", { name: /Send 1 comment/ })).toHaveCount(0)
  })

  test("sidebar status dot: accent arc while working, solid dot when finished unread, warn for needs you", async ({ page }) => {
    await desktop(page)
    await page.route("**/api/sessions", (route) => {
      if (route.request().method() !== "GET") return route.fallback()
      return route.fulfill({
        json: [
          { ...session, transport: "acp", conversation_state: "working" },
          { ...session, id: "local-two", name: "Another", transport: "acp", conversation_state: "needs_you" },
          // Activity after this browser's first-visit baseline: finished and unread.
          { ...session, id: "local-three", name: "Finished", transport: "acp", conversation_state: "idle", last_event_at: "2099-01-01T00:00:00Z" },
          // Its turn ended with background work open: waiting, neither working nor finished.
          { ...session, id: "local-four", name: "Nightly e2e", transport: "acp", conversation_state: "idle", waiting: [{ kind: "shell" }, { kind: "monitor" }] },
        ],
      })
    })
    await page.goto("/sessions")
    const token = (value: string) =>
      page.evaluate((value) => {
        const probe = document.createElement("div")
        probe.style.background = value
        document.body.appendChild(probe)
        const color = getComputedStyle(probe).backgroundColor
        probe.remove()
        return color
      }, value)
    const working = page.locator('[data-row-key="local:local-one"] [data-slot="agent-dot"]')
    await expect(working).toHaveAttribute("data-state", "working")
    expect(await style(working, "animation-name")).toBe("dot-spin")
    expect(await style(working, "border-right-color")).toBe(await token("var(--accent)"))
    expect(await style(working, "border-top-color")).toBe("rgba(0, 0, 0, 0)")
    const done = page.locator('[data-row-key="local:local-three"] [data-slot="agent-dot"]')
    await expect(done).toHaveAttribute("data-state", "done")
    expect(await style(done, "background-color")).toBe(await token("var(--accent)"))
    expect(await style(page.locator('[data-row-key="local:local-three"] span.flex-1'), "font-weight")).toBe("600")
    const needs = page.locator('[data-row-key="local:local-two"] [data-slot="agent-dot"]')
    await expect(needs).toHaveAttribute("data-state", "needs")
    expect(await style(needs, "background-color")).toBe(await token("var(--warn)"))
    // The name gets the row: no agent or status words beside it except the one trailing slot.
    await expect(page.locator('[data-row-key="local:local-two"]')).toContainText("Needs you")
    const waiting = page.locator('[data-row-key="local:local-four"] [data-slot="agent-dot"]')
    await expect(waiting).toHaveAttribute("data-state", "waiting")
    expect(await style(waiting, "animation-name")).toBe("none")
    expect(await style(waiting, "border-top-color")).toBe(await token("var(--accent)"))
    await expect(page.locator('[data-row-key="local:local-four"]')).toContainText("2 running")
    await expect(page.getByRole("region", { name: "Waiting" })).toContainText("Nightly e2e")
    await expect(page.getByRole("region", { name: "Waiting" })).toContainText("2 background tasks")
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
    // Idle shows as the agent tile with no dot; its name is on the tile.
    await expect(page.getByRole("img", { name: "Idle" }).first()).toBeAttached()
    await page.goto("/sessions/local/local-one")
    await page.getByRole("button", { name: "Commands" }).click()
    await expect(page.locator('[data-slot="composer-hints"]')).toBeVisible()
    const message = page.getByRole("textbox", { name: "Message" })
    await message.fill("/ter")
    await expect(page.locator('[data-slot="composer-hints"]')).toContainText("/terminal")
    // A command is marked behind the textarea, aligned with the text it covers.
    await message.fill("/terminal now")
    const mark = page.locator("mark[data-command]")
    await expect(mark).toHaveText("/terminal")
    expect(await style(mark, "background-color")).not.toBe("rgba(0, 0, 0, 0)")
    expect(await style(mark, "color")).toBe("rgba(0, 0, 0, 0)")
    const [markBox, inputBox] = [await mark.boundingBox(), await message.boundingBox()]
    expect(Math.abs(markBox!.y - inputBox!.y - Number.parseFloat(await style(message, "padding-top")))).toBeLessThan(4)
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

  test("Project settings lists Library packages for the selected session folder", async ({ page }) => {
    await desktop(page)
    const pin = { module: "github.com/acme/tools", commit: "a".repeat(40), content_hash: "", cache_key: "k", kind: "skill", name: "tools", url: "", direct: true }
    await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow("/work/my-project", [{ harness: "claude" }], { packages: [pin] }), projectRow("/work/another-project", [{ harness: "claude" }])] }))
    await page.route("**/api/library/packages", (route) => route.fulfill({ json: [{ ...pin, id: "k", owner: "acme", repo: "tools" }] }))
    const posted: { add?: string[]; directory?: string }[] = []
    await page.route("**/api/projects/settings", (route) => {
      posted.push(route.request().postDataJSON())
      return route.fulfill({ status: 204 })
    })
    await page.goto("/sessions")
    const row = page.locator('[data-row-key="local:local-two"]')
    await row.hover()
    await page.getByRole("button", { name: "More actions" }).first().click()
    await page.getByRole("menuitem", { name: "Project settings…" }).click()
    const known = page.getByRole("group", { name: "Library packages" })
    const add = known.getByRole("button", { name: "Add tools" })
    await expect(add).toBeVisible()
    await add.click()
    await page.getByRole("button", { name: "Save", exact: true }).click()
    await expect.poll(() => posted.length).toBe(1)
    expect(posted[0]).toMatchObject({ add: [`github.com/acme/tools@${"a".repeat(40)}`], directory: "/work/another-project" })
  })
})

test("a machine list HTTP error leaves the gateway connection item hidden", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ status: 400, json: { error: "tailscale is not running" } }))
  await page.goto("/sessions")
  await expect(page.locator('[data-slot="toolbar"]')).toContainText(/\d+ sessions? on 1 machine/)
  await expect(page.getByText("Discovering machines…")).toHaveCount(0)
  await expect(page.getByRole("button", { name: "Gateway unreachable" })).toHaveCount(0)
})

// Peers update on their own schedule, so the gateway routinely talks to older ones.
test.describe("a peer on an older version", () => {
  test.skip(({ isMobile }) => isMobile, "desktop")

  async function oldPeer(page: Page) {
    await desktop(page)
    await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "old", name: "main", online: true }] }))
    const unlessPeer = (json: unknown, peerJson: unknown, status = 200) => (route: Route) =>
      // Go's mux answers a missing route in plain text, not JSON.
      new URL(route.request().url()).searchParams.has("node") ? route.fulfill(typeof peerJson === "string" ? { status, body: peerJson } : { status, json: peerJson }) : route.fulfill({ json })
    // It ignores ?include=revisions and has no /api/projects.
    await page.route("**/api/library/agentpacks**", unlessPeer([], [{ id: "bare", name: "bare-pack", revisions: ["r1"], heads: ["r1"] }]))
    await page.route("**/api/projects**", unlessPeer([], "404 page not found", 404))
    await page.route("**/api/machine?**", unlessPeer({}, "404 page not found", 404))
  }

  test("the Library lists its bare agentpacks instead of crashing", async ({ page }) => {
    await oldPeer(page)
    await page.goto("/library")
    await expect(page.getByRole("link", { name: /bare-pack/ })).toBeVisible()
  })

  test("Machines asks for an update, with no raw 404", async ({ page }) => {
    await oldPeer(page)
    await page.goto("/machines/old")
    await expect(page.getByRole("alert")).toContainText("Update Agentpack on main to see its agents and system details.")
    await expect(page.getByText("404")).toHaveCount(0)
  })
})

test("a confirm alert places macOS-order buttons inside it on desktop and stacks them on phone", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  await page.goto("/settings/account")
  await page.getByRole("button", { name: "Stop gateway…" }).click()
  const dialog = page.getByRole("alertdialog")
  const cancelBtn = dialog.getByRole("button", { name: "Cancel" })
  const stopBtn = dialog.getByRole("button", { name: "Stop gateway" })
  await expect(cancelBtn).toBeFocused()
  // Measure together so the opening animation cannot move the alert between reads.
  const measure = () => dialog.evaluate((el) => {
    const buttons = [...el.querySelectorAll("button")]
    const cancel = buttons.find((b) => b.textContent?.trim() === "Cancel")!.getBoundingClientRect()
    const stop = buttons.find((b) => b.textContent?.trim() === "Stop gateway")!.getBoundingClientRect()
    const alert = el.getBoundingClientRect()
    return {
      alert: { x: alert.x, right: alert.right },
      cancel: { x: cancel.x, y: cancel.y, right: cancel.right },
      stop: { x: stop.x, y: stop.y, right: stop.right },
    }
  })
  const desk = await measure()
  expect(desk.cancel.x).toBeGreaterThanOrEqual(desk.alert.x)
  expect(desk.stop.right).toBeLessThanOrEqual(desk.alert.right)
  expect(desk.stop.x).toBeGreaterThan(desk.cancel.x)
  expect(Math.abs(desk.cancel.y - desk.stop.y)).toBeLessThan(2)
  // Filled red destructive action on desktop and phone.
  const danger = await page.evaluate(() => {
    const probe = document.createElement("span")
    probe.style.backgroundColor = getComputedStyle(document.documentElement).getPropertyValue("--danger").trim()
    document.body.append(probe)
    const color = getComputedStyle(probe).backgroundColor
    probe.remove()
    return color
  })
  expect(await style(stopBtn, "background-color")).toBe(danger)

  // Phone stacks the destructive action above Cancel, still inside the alert.
  await page.setViewportSize({ width: 390, height: 844 })
  if (!(await dialog.isVisible())) await page.getByRole("button", { name: "Stop gateway…" }).click()
  await expect(dialog).toBeVisible()
  const phone = await measure()
  expect(phone.stop.x).toBeGreaterThanOrEqual(phone.alert.x)
  expect(phone.stop.right).toBeLessThanOrEqual(phone.alert.right)
  expect(phone.cancel.y).toBeGreaterThan(phone.stop.y)
})

test("a machine with no projects says so in New session", async ({ page, isMobile }) => {
  test.skip(isMobile, "desktop")
  await desktop(page)
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [] }))
  await page.goto("/sessions?new=1")
  await expect(page.getByText("No projects yet")).toBeVisible()
  await expect(page.getByRole("navigation", { name: "Projects" }).getByRole("button")).toHaveCount(0)
  await expect(page.getByRole("button", { name: "Start session" })).toBeDisabled()
})

test("a grid column can be dragged wider", async ({ page, isMobile, browserName }) => {
  test.skip(isMobile || browserName !== "chromium", "desktop pointer")
  await desktop(page)
  await page.goto("/machines/topology")
  const header = page.getByRole("columnheader", { name: "Machine" })
  const before = (await header.boundingBox())!
  const handle = (await page.getByRole("separator", { name: "Resize Machine" }).boundingBox())!
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
  // Measure together so the opening animation cannot move the sheet between reads.
  const overflow = await page.getByRole("dialog").getByText(long, { exact: true }).last().evaluate((project) => {
    const sheet = project.closest('[role="dialog"]')!
    const start = sheet.querySelector('button[type="submit"]')!
    return Math.max(start.getBoundingClientRect().right, project.getBoundingClientRect().right) - sheet.getBoundingClientRect().right
  })
  expect(overflow).toBeLessThanOrEqual(0)
  // A CLI launch records an empty mode; the control still names the real one.
  await expect(page.getByRole("group", { name: "Mode" }).getByRole("button", { name: "default" })).toHaveAttribute("aria-pressed", "true")
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
  await page.getByRole("menuitem", { name: "Stop agent…" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Stop agent" }).click()
  await expect(wrapper).toContainText("Stopping…")
  finish()
  await expect(wrapper).not.toContainText("Stopping…")
})
