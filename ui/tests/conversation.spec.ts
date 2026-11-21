import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"
import { expect, test, type Page } from "@playwright/test"
import { conversationImage, identity, session } from "./helpers"

const fixturePath = join(dirname(fileURLToPath(import.meta.url)), "../../testdata/acp/full.jsonl")
const fixtureBody = readFileSync(fixturePath, "utf8")

async function sendImagePrompt(page: Page, supportsImages: boolean) {
  let posted: Record<string, unknown> | undefined
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({
      json: [{
        ...session,
        id: "chat-one",
        transport: "acp",
        agent_capabilities: { promptCapabilities: { image: supportsImages } },
      }],
    })
  })
  await page.route("**/api/sessions/*/conversation**", (route) =>
    route.fulfill({ body: "", contentType: "application/x-ndjson" }),
  )
  await page.route("**/api/sessions/*/conversation/blobs", (route) => route.fulfill({ json: { sha: "a".repeat(64) } }))
  await page.route("**/api/sessions/*/conversation/prompt", (route) => {
    posted = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  await page.locator('input[type="file"]').setInputFiles({
    name: "diagram.png",
    mimeType: "image/png",
    buffer: conversationImage,
  })
  await expect(page.getByText("Image attached")).toBeVisible()
  await page.getByRole("button", { name: "Send" }).click()
  await expect.poll(() => posted).toBeDefined()
  return posted!
}

test.beforeEach(async ({ page }) => {
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", transport: "acp" }] })
  })
  await page.route("**/api/sessions/*/conversation**", (route) =>
    route.fulfill({ body: fixtureBody, contentType: "application/x-ndjson" }),
  )
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/sessions/*/blobs/**", (route) =>
    route.fulfill({ body: conversationImage, contentType: "image/png" }),
  )
})

test("conversation thread renders the latest reply, expandable plan, image, and permission card", async ({ page }) => {
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("I'll update the reattach path")).toBeVisible()
  await expect(page.getByText("Done. The gateway will resume from the saved offset.")).toBeInViewport()
  const image = page.locator('img[src*="/blobs/"]')
  await expect(image).toBeVisible()
  const plan = page.locator('[data-slot="plan-pill"]')
  await expect(plan).toContainText("Publish the branch")
  await expect(plan).toContainText("1 of 2")
  await expect(page.getByText('"entries"', { exact: false })).toHaveCount(0)
  await expect(plan).not.toHaveAttribute("open")
  await expect(plan.locator("ol")).toBeHidden()
  await plan.locator("summary").click()
  await expect(plan.locator("ol")).toBeVisible()
  await expect(plan).toContainText("Reproduce with a restarted gateway")
  await expect(page.locator('[data-slot="permission-card"]')).toBeVisible()
  await expect(page.getByRole("button", { name: "Allow once" })).toBeVisible()
  await expect(page.locator('[data-slot="conversation-inspector"]')).toBeVisible()
  await expect(page.getByRole("button", { name: "Hide session details" })).toBeVisible()
})
test("inspector shows only capabilities advertised by the agent", async ({ page }) => {
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({
      json: [{
        ...session,
        id: "chat-one",
        transport: "acp",
        agent_capabilities: {
          loadSession: true,
          promptCapabilities: { image: false },
          sessionCapabilities: { resume: {}, fork: {} },
        },
      }],
    })
  })
  await page.goto("/sessions/local/chat-one")
  const inspector = page.locator('[data-slot="conversation-inspector"]')
  await expect(inspector.getByText("This agent supports")).toBeVisible()
  await expect(inspector).toContainText("Images")
  await expect(inspector).toContainText("No")
  await expect(inspector).toContainText("Resume")
  await expect(inspector).toContainText("Fork")
})

test("uploads use resource_link unless the agent advertises image blocks", async ({ page }) => {
  const linked = await sendImagePrompt(page, false)
  expect(linked).toEqual({
    text: "",
    resource_link: { sha: "a".repeat(64), name: "diagram.png" },
  })
})

test("agents advertising image blocks receive the image block", async ({ page }) => {
  const sent = await sendImagePrompt(page, true)
  expect(sent).toMatchObject({
    text: "",
    image: { sha: "a".repeat(64), mime: "image/png", width: 640, height: 300 },
  })
  expect(sent).not.toHaveProperty("resource_link")
})

test("generic elicitation shows option labels and answers with the ACP option ID", async ({ page }) => {
  const event = '{"seq":99,"ts":1099,"kind":"elicitation","payload":{"elicitationId":"question-1","prompt":"Choose a workspace","choices":["Local","Remote"],"optionIds":["local-id","remote-id"]}}\n'
  let posted: Record<string, unknown> | undefined
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: event, contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/elicitation", (route) => {
    posted = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  const card = page.locator('[data-slot="elicitation-card"]')
  await expect(card).toContainText("Choose a workspace")
  await expect(card.getByRole("button", { name: "Local" })).toBeVisible()
  await card.getByRole("button", { name: "Local" }).click()
  await expect.poll(() => posted).toBeDefined()
  expect(posted).toEqual({ elicitation_id: "question-1", value: "local-id" })
})

test("multi-choice elicitation submits the selected ACP option IDs", async ({ page }) => {
  const event = '{"seq":100,"ts":1100,"kind":"elicitation","payload":{"elicitationId":"question-2","prompt":"Choose destinations","choices":["Local","Remote"],"optionIds":["local-id","remote-id"],"allowMultiple":true}}\n'
  let posted: Record<string, unknown> | undefined
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: event, contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/elicitation", (route) => {
    posted = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  const card = page.locator('[data-slot="elicitation-card"]')
  await card.getByRole("button", { name: "Remote" }).click()
  await card.getByRole("button", { name: "Local" }).click()
  await card.getByRole("button", { name: "Submit" }).click()
  await expect.poll(() => posted).toBeDefined()
  expect(posted).toEqual({ elicitation_id: "question-2", value: '["remote-id","local-id"]' })
})

test("AskUserQuestion form answers every question and leaves the composer usable", async ({ page }) => {
  const fields = [
    { key: "question_0", title: "Commit", description: "Commit the work?", options: [{ value: "Commit everything", label: "Commit everything" }, { value: "Don't commit", label: "Don't commit" }] },
    { key: "question_0_custom", title: "Other" },
    { key: "question_1", title: "Login", multiple: true, options: [{ value: "Linear", label: "Linear" }, { value: "Skip", label: "Skip" }] },
  ]
  const event = JSON.stringify({ seq: 101, ts: 1101, kind: "elicitation", payload: { elicitationId: "question-3", prompt: "Please answer the following questions.", fields } }) + "\n"
  let posted: Record<string, unknown> | undefined
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: event, contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/elicitation", (route) => {
    posted = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  const card = page.locator('[data-slot="elicitation-card"]')
  await expect(card).toContainText("Commit the work?")
  await expect(page.getByRole("button", { name: "Attach image" })).toBeEnabled()
  await card.getByRole("button", { name: "Commit everything" }).click()
  await card.getByRole("textbox", { name: "Other" }).fill("and push")
  await card.getByRole("button", { name: "Linear" }).click()
  await card.getByRole("button", { name: "Submit" }).click()
  await expect.poll(() => posted).toBeDefined()
  expect(JSON.parse(posted!.value as string)).toEqual({ question_0: "Commit everything", question_0_custom: "and push", question_1: ["Linear"] })
})

test("ACP mode fallback shows and updates only actual mode state when configOptions is absent", async ({ page }) => {
  let posted: Record<string, unknown> | undefined
  await page.route("**/api/sessions/*/conversation**", (route) =>
    route.fulfill({ body: "", contentType: "application/x-ndjson" }),
  )
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({
    json: {
      modes: {
        currentModeId: "plan",
        availableModes: [{ id: "default", name: "Default" }, { id: "plan", name: "Plan" }],
      },
    },
  }))
  await page.route("**/api/sessions/*/conversation/config", (route) => {
    posted = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  const permissions = page.getByRole("combobox", { name: "Permissions" })
  await expect(permissions).toContainText("Plan")
  await expect(page.getByRole("combobox", { name: "Model" })).toHaveCount(0)
  await permissions.click()
  await page.getByRole("option", { name: "Default" }).click()
  await expect.poll(() => posted).toBeDefined()
  expect(posted).toEqual({ key: "mode", value: "default" })
})

test("boolean ACP config options send a JSON boolean", async ({ page }) => {
  let posted: Record<string, unknown> | undefined
  await page.route("**/api/sessions/*/conversation**", (route) =>
    route.fulfill({ body: "", contentType: "application/x-ndjson" }),
  )
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({
    json: {
      configOptions: [{ type: "boolean", id: "verbose", name: "Verbose output", currentValue: false }],
    },
  }))
  await page.route("**/api/sessions/*/conversation/config", (route) => {
    posted = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  await page.getByRole("button", { name: "Options" }).click()
  const toggle = page.getByRole("menuitemcheckbox", { name: "Verbose output" })
  await expect(toggle).toHaveAttribute("aria-checked", "false")
  await toggle.click()
  await expect.poll(() => posted).toBeDefined()
  expect(posted).toEqual({ key: "verbose", value: true })
})

test("permission shortcuts answer only from the focused card or when no control has focus", async ({ page }) => {
  const answers: Record<string, unknown>[] = []
  await page.route("**/api/sessions/*/conversation/permission", (route) => {
    answers.push(route.request().postDataJSON() as Record<string, unknown>)
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  const card = page.locator('[data-slot="permission-card"]').first()
  await page.getByRole("button", { name: "Hide session details" }).focus()
  await page.keyboard.press("1")
  expect(answers).toHaveLength(0)
  await card.focus()
  await page.keyboard.press("1")
  await expect.poll(() => answers.length).toBe(1)
  expect(answers[0]).toMatchObject({ request_id: "p1", option_id: "allow" })
})

test("resolved permission and elicitation cards show their result without answer buttons", async ({ page }) => {
  await page.route("**/api/sessions/*/conversation**", (route) =>
    route.fulfill({
      body: `${fixtureBody}{"seq":24,"ts":1023,"kind":"permission_result","payload":{"requestId":"p1","optionId":"allow"}}\n{"seq":25,"ts":1024,"kind":"elicitation_result","payload":{"elicitationId":"e1","value":"Direct"}}\n`,
      contentType: "application/x-ndjson",
    }),
  )
  await page.goto("/sessions/local/chat-one")
  const permission = page.locator('[data-slot="permission-card"]')
  const elicitation = page.locator('[data-slot="elicitation-card"]')
  await expect(permission).toContainText("Answered: Allow once")
  await expect(permission.getByRole("button")).toHaveCount(0)
  await expect(elicitation).toContainText("Answered: Direct")
  await expect(elicitation.getByRole("button")).toHaveCount(0)
})

test("history keeps diff and terminal output collapsed until opened", async ({ page }) => {
  await page.goto("/sessions/local/chat-one")
  const group = page.locator('[data-slot="tool-group"]')
  await expect(group).not.toHaveAttribute("open")
  await group.locator(":scope > summary").click()
  await expect(group).toContainText("Worked")
  await expect(group.locator("[data-tool-row]")).toHaveCount(6)
  const diff = page.locator('[data-slot="tool-diff-block"]')
  const terminal = page.locator('[data-slot="tool-terminal-block"]')
  await expect(diff).not.toHaveAttribute("open")
  await expect(terminal).not.toHaveAttribute("open")
  await expect(page.locator('[data-slot="tool-terminal"]')).toHaveCount(0)
  await terminal.locator(":scope > summary").click()
  await expect(page.locator('[data-slot="tool-terminal"]')).toContainText("ok")
})

test("/terminal reopens a stopped chat as a terminal session", async ({ page }) => {
  let posted: unknown
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() === "POST") {
      posted = route.request().postDataJSON()
      return route.fulfill({ json: { ...session, id: "term-one", transport: "terminal", state: { ...session.state, running: true } } })
    }
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({
      json: [{ ...session, id: "chat-one", transport: "acp", state: { ...session.state, running: false } }],
    })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByRole("alert")).toHaveCount(0)
  // The toolbar has no Chat/Terminal switch; the composer command is the way.
  await expect(page.getByRole("group", { name: "Session view" })).toHaveCount(0)
  await page.getByRole("textbox", { name: "Message" }).fill("/terminal")
  await page.keyboard.press("Enter")
  await expect(page).toHaveURL(/term-one/)
  expect(posted).toMatchObject({ transport: "terminal", directory: session.directory })
})

test("a fork opens the claimed chat while its request is pending", async ({ page }) => {
  let release!: () => void
  const response = new Promise<void>((resolve) => { release = resolve })
  const source = {
    ...session, id: "chat-one", transport: "acp", agent_session_id: "native-chat",
    agent_capabilities: { sessionCapabilities: { fork: {} } },
  }
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [source] }))
  let id = ""
  await page.route("**/api/sessions/chat-one/fork", async (route) => {
    id = route.request().postDataJSON().id
    await response
    await route.fulfill({ status: 409, body: "Cannot fork" })
  })
  await page.goto("/sessions/local/chat-one")
  await page.getByRole("textbox", { name: "Message" }).fill("/fork")
  await page.keyboard.press("Enter")
  await expect(page).toHaveURL(/\/sessions\/local\/session-[0-9a-f]{32}$/)
  await expect.poll(() => id).toMatch(/^session-[0-9a-f]{32}$/)
  await expect(page).toHaveURL(new RegExp(`${id}$`))
  release()
  await expect(page).toHaveURL(/\/sessions\/local\/chat-one$/)
})

test("a streamed permission result resolves the card without another snapshot", async ({ page }) => {
  let snapshots = 0
  let resolveAnswer!: () => void
  const answered = new Promise<void>((resolve) => { resolveAnswer = resolve })
  await page.route("**/api/sessions/chat-one/conversation**", async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith("/config-options")) return route.fulfill({ json: [] })
    if (!url.searchParams.has("stream")) {
      snapshots++
      return route.fulfill({ body: fixtureBody, contentType: "application/x-ndjson" })
    }
    await answered
    return route.fulfill({
      body: 'id: 99\ndata: {"seq":99,"ts":1099,"kind":"permission_result","payload":{"requestId":"p1","optionId":"allow"}}\n\n',
      contentType: "text/event-stream",
    })
  })
  await page.route("**/api/sessions/*/conversation/permission", async (route) => {
    await route.fulfill({ json: { accepted: true } })
    resolveAnswer()
  })
  await page.goto("/sessions/local/chat-one")
  const allow = page.getByRole("button", { name: "Allow once" })
  await expect(allow).toBeVisible()
  const before = snapshots
  await allow.click()
  await expect(page.getByText(/Answered: Allow once/)).toBeVisible()
  expect(snapshots).toBe(before)
})

test("a failed permission answer restores the choices", async ({ page }) => {
  let release!: () => void
  const response = new Promise<void>((resolve) => { release = resolve })
  const ids: string[] = []
  await page.route("**/api/sessions/*/conversation/permission", async (route) => {
    ids.push(route.request().headers()["idempotency-key"] ?? "")
    if (ids.length > 1) return route.fulfill({ json: { accepted: true } })
    await response
    await route.fulfill({ status: 409, body: "Answer rejected" })
  })
  await page.goto("/sessions/local/chat-one")
  const allow = page.getByRole("button", { name: "Allow once" })
  await allow.click()
  await expect(page.getByText("Sending answer…")).toBeVisible()
  await expect(allow).toHaveCount(0)
  release()
  await expect(allow).toBeEnabled()
  await page.getByRole("button", { name: "Retry", exact: true }).click()
  await expect(page.getByText(/Answered: Allow once/)).toBeVisible()
  expect(ids).toEqual([expect.stringMatching(/^[a-f0-9]{32}$/), ids[0]])
})

test("escape closes message context menu without posting permission", async ({ page }) => {
  let permissionPosts = 0
  await page.route("**/api/sessions/*/conversation/permission", (route) => {
    permissionPosts += 1
    return route.fallback()
  })
  await page.goto("/sessions/local/chat-one")
  await page.getByText("I'll update the reattach path").click({ button: "right" })
  await expect(page.getByRole("menuitem", { name: "Copy" })).toBeVisible()
  await page.keyboard.press("1")
  await expect(page.getByRole("menuitem", { name: "Copy" })).toBeVisible()
  await page.keyboard.press("Escape")
  await expect(page.getByRole("menuitem", { name: "Copy" })).toHaveCount(0)
  expect(permissionPosts).toBe(0)
  const card = page.locator('[data-slot="permission-card"]').first()
  await card.focus()
  await page.keyboard.press("Escape")
  expect(permissionPosts).toBe(0)
  await expect(page.getByRole("button", { name: "Allow once" })).toBeVisible()
})

test("phone layout keeps tabs and opens the session inspector on demand", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("I'll update the reattach path")).toBeVisible()
  const tabs = page.getByRole("navigation", { name: "Tabs" })
  await expect(tabs).toBeVisible()
  await expect(page.getByRole("button", { name: "Chat", exact: true })).toHaveCount(0)
  await expect(page.getByRole("button", { name: "Terminal", exact: true })).toHaveCount(0)
  await expect(tabs.getByRole("link")).toHaveCount(5)
  const inspector = page.locator('[data-slot="conversation-inspector"]')
  await expect(inspector).toBeHidden()
  await page.getByRole("button", { name: "Show session details" }).click()
  await expect(inspector).toBeVisible()
  expect((await inspector.boundingBox())?.width).toBe(272)
  await page.getByRole("button", { name: "Hide session details" }).click()
  await expect(inspector).toBeHidden()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
})

test("a missing pane chunk reloads once, then offers Reload instead of loading forever", async ({ page }) => {
  let loads = 0
  page.on("load", () => { loads += 1 })
  await page.route("**/conversation-pane-*.js", (route) => route.fulfill({ status: 404, body: "" }))
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByRole("button", { name: "Reload" })).toBeVisible()
  expect(loads).toBe(2)
})

test("a stalled pane chunk offers Reload after 15 s", async ({ page }) => {
  await page.clock.install()
  await page.route("**/conversation-pane-*.js", () => {}) // never answered
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("Loading conversation…")).toBeAttached()
  await page.clock.fastForward(16_000)
  await expect(page.getByRole("button", { name: "Reload" })).toBeVisible()
})

test("scrolling up loads earlier history and keeps the reader in place", async ({ page }) => {
  const line = (seq: number) =>
    JSON.stringify(seq % 2
      ? { seq, ts: seq, kind: "user_message_chunk", payload: { text: { text: `Question ${seq}` } } }
      : { seq, ts: seq, kind: "agent_message_chunk", payload: { text: { text: `Answer ${seq}\n\nwith a second paragraph` } } })
  const body = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => `${line(from + i)}\n`).join("")
  const befores: string[] = []
  await page.route("**/api/sessions/*/conversation**", (route) => {
    const before = new URL(route.request().url()).searchParams.get("before")
    if (!before) return route.fulfill({ body: body(41, 80), contentType: "application/x-ndjson", headers: { "X-Agentpack-Truncated": "true" } })
    befores.push(before)
    return route.fulfill({ body: body(1, 40), contentType: "application/x-ndjson" })
  })
  await page.goto("/sessions/local/chat-one")
  const thread = page.getByRole("region", { name: "Conversation" }).or(page.getByLabel("Conversation", { exact: true })).first()
  await expect(page.getByText("Answer 80")).toBeInViewport()
  await expect(page.getByText("Question 1", { exact: true })).toHaveCount(0)

  await thread.evaluate((element) => (element.scrollTop = 0))
  await expect(page.getByText("Beginning of conversation")).toHaveCount(1)
  await expect(page.getByText("Question 1", { exact: true })).toHaveCount(1)
  // The message the reader was on stays on screen; older ones are above it.
  await expect(page.getByText("Question 41", { exact: true })).toBeInViewport()
  expect(befores).toEqual(["41"])
})

const subagentsBody = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "../../testdata/acp/subagents.jsonl"), "utf8")

test("attached images survive switching chats and coming back", async ({ page }) => {
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", transport: "acp" }, { ...session, id: "chat-two", transport: "acp", name: "Other chat" }] })
  })
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: "", contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/sessions/*/conversation/blobs", (route) => route.fulfill({ json: { sha: "b".repeat(64) } }))
  await page.goto("/sessions/local/chat-one")
  await page.locator('input[type="file"]').setInputFiles({ name: "diagram.png", mimeType: "image/png", buffer: conversationImage })
  await expect(page.getByText("Image attached · diagram.png")).toBeVisible()
  await page.goto("/sessions/local/chat-two")
  await expect(page.getByText("Image attached")).toHaveCount(0)
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("Image attached · diagram.png")).toBeVisible()
  // The restored preview comes from the server, not a dead blob: URL.
  await expect(page.locator(`img[src*="/blobs/${"b".repeat(64)}"]`)).toBeVisible()
})

test("history replay does not animate", async ({ page }) => {
  await page.clock.install()
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("Done. The gateway will resume from the saved offset.")).toBeVisible()
  await expect(page.locator(".thread-enter, .tool-enter")).toHaveCount(0)
})

test("subagents show as cards, open in the main view, and Back returns", async ({ page }) => {
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", transport: "acp", subagents: [{ id: "sub-review", title: "Review", state: "running", started_at: Date.now() }] }] })
  })
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: subagentsBody, contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
  await page.goto("/sessions/local/chat-one")
  const cards = page.locator('[data-slot="subagent-card"]')
  await expect(cards).toHaveCount(2)
  await expect(page.getByText("2 subagents")).toBeVisible()
  // Child output stays in the child.
  await expect(page.getByText("Searching the worker routes.")).toHaveCount(0)

  await page.getByRole("button", { name: "Open Explore" }).click()
  await expect(page).toHaveURL(/sub=sub-explore/)
  await expect(page.getByText("Searching the worker routes.")).toBeVisible()
  await expect(page.locator('[data-slot="subagent-task"]')).toContainText("Find where POST /conversation/cancel is handled.")
  await page.getByRole("button", { name: "Back to the conversation" }).click()
  await expect(page).not.toHaveURL(/sub=/)

  // From the inspector, then the browser's back button.
  await page.locator('[data-slot="conversation-inspector"]').getByRole("button", { name: /Review/ }).click()
  await expect(page).toHaveURL(/sub=sub-review/)
  await page.goBack()
  await expect(page).not.toHaveURL(/sub=/)

  // From the sidebar child row, then Esc.
  await page.locator('[data-subagent-row="sub-review"]').click()
  await expect(page).toHaveURL(/sub=sub-review/)
  await page.locator("body").press("Escape")
  await expect(page).not.toHaveURL(/sub=/)
})

test("phone: a subagent's sidebar row opens it full-screen", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", transport: "acp", subagents: [{ id: "sub-review", title: "Review", state: "running" }] }] })
  })
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: subagentsBody, contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
  await page.goto("/sessions")
  await page.locator('[data-subagent-row="sub-review"]').click()
  await expect(page).toHaveURL(/sub=sub-review/)
  await expect(page.getByText("Running the cancel tests.")).toBeVisible()
})

test("Enter queues while the agent works and ⌘Enter steers", async ({ page }) => {
  const posts: Record<string, unknown>[] = []
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", transport: "acp" }] })
  })
  const working = [
    { seq: 1, ts: 1000, kind: "prompt", payload: { text: { text: "Go" } } },
    { seq: 2, ts: 1001, kind: "agent_message_chunk", payload: { text: { text: "On it" } } },
    { seq: 3, ts: 1002, kind: "queued_prompt", payload: { queueId: "q1", text: { text: "Then add tests" } } },
  ].map((entry) => JSON.stringify(entry)).join("\n")
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: working, contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/sessions/*/conversation/prompt", (route) => {
    posts.push(route.request().postDataJSON() as Record<string, unknown>)
    return route.fulfill({ json: { accepted: true, queued: "q2" } })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.locator('[data-slot="prompt-queue"]')).toContainText("Then add tests")
  await expect(page.getByRole("button", { name: "Steer now" })).toBeVisible()
  const message = page.getByRole("textbox", { name: "Message" })
  await message.fill("queue me")
  await message.press("Enter")
  await expect.poll(() => posts.length).toBe(1)
  expect(posts[0]).toMatchObject({ text: "queue me" })
  expect(posts[0]!.steer).toBeUndefined()
  await message.fill("steer me")
  await message.press("ControlOrMeta+Enter")
  await expect.poll(() => posts.length).toBe(2)
  expect(posts[1]).toMatchObject({ text: "steer me", steer: true })
  let steered = ""
  let release!: () => void
  const response = new Promise<void>((resolve) => { release = resolve })
  await page.route("**/api/sessions/*/conversation/queue/*/steer", async (route) => {
    steered = route.request().url()
    await response
    return route.fulfill({ status: 409, body: "Cannot steer" })
  })
  await message.fill("")
  await message.press("Enter")
  await expect(page.getByText("Steering next:", { exact: true })).toBeVisible()
  await expect.poll(() => steered).toContain("/queue/q1/steer")
  release()
  await expect(page.getByRole("button", { name: "Steer now" })).toBeEnabled()
  expect(posts).toHaveLength(2)
})

test("the composer continues a chat with another agent", async ({ page }) => {
  let body: unknown
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", transport: "acp", harness: "claude" }] })
  })
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: "", contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/sessions/chat-one/harness", (route) => {
    body = route.request().postDataJSON()
    return route.fulfill({ json: { ...session, id: "chat-one", transport: "acp", harness: "codex" } })
  })
  await page.goto("/sessions/local/chat-one")
  await page.getByRole("button", { name: "Claude", exact: true }).click()
  await expect(page.getByRole("menuitemradio", { name: "Claude" })).toBeDisabled()
  await page.getByRole("menuitemradio", { name: "Codex" }).click()
  await expect.poll(() => body).toEqual({ harness: "codex" })
})

test("a starting chat shows the last agent choices before its worker is ready", async ({ page }) => {
  await page.route("**/api/sessions", (route) => route.fulfill({
    json: [{ ...session, id: "chat-one", transport: "acp", conversation_state: "starting", state: { ...session.state, running: false } }],
  }))
  await page.route("**/api/sessions/chat-one/conversation**", (route) =>
    route.fulfill({ body: "", contentType: "application/x-ndjson" }),
  )
  await page.route("**/api/agent-snapshot?*", (route) => route.fulfill({
    json: {
      config: {
        configOptions: [{
          type: "select", id: "model", name: "Model", category: "model", currentValue: "sonnet",
          options: [{ value: "sonnet", name: "Sonnet" }, { value: "opus", name: "Opus" }],
        }],
      },
      commands: { availableCommands: [{ name: "review" }] },
    },
  }))
  let posted: unknown
  await page.route("**/api/sessions/chat-one/conversation/config", (route) => {
    posted = route.request().postDataJSON()
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("Provisional", { exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Model", exact: true }).click()
  await page.getByRole("menuitemradio", { name: "Opus" }).click()
  await expect.poll(() => posted).toEqual({ key: "model", value: "opus" })
  await expect(page.getByRole("button", { name: "Model", exact: true })).toHaveText("Opus")
})

test("a sent message shows before the gateway answers, and a failure puts it back in the composer", async ({ page }) => {
  let release: (() => void) | undefined
  const idle = [
    { seq: 1, ts: 1000, kind: "prompt", payload: { text: { text: "Go" } } },
    { seq: 2, ts: 1001, kind: "turn_end", payload: { stopReason: "end_turn" } },
  ].map((entry) => JSON.stringify(entry)).join("\n")
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: idle, contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/sessions/*/conversation/prompt", (route) => new Promise<void>((resolve) => {
    release = () => resolve(route.fulfill({ status: 500, body: "agent unavailable" }))
  }))
  await page.goto("/sessions/local/chat-one")
  const message = page.getByRole("textbox", { name: "Message" })
  await message.fill("ship the fix")
  await message.press("Enter")
  const thread = page.getByLabel("Conversation")
  await expect(message).toHaveValue("")
  await expect(thread.getByText("ship the fix")).toBeVisible()
  await expect.poll(() => release).toBeDefined()
  release!()
  await expect(message).toHaveValue("ship the fix")
  await expect(thread.getByText("ship the fix")).toHaveCount(0)
})

test("a stopped chat shows why it stopped, not an update banner", async ({ page }) => {
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", harness: "agy", transport: "acp", ended_at: session.started_at, state: { ...session.state, running: false, exit_code: 1 } }] })
  })
  const notice = { sessionUpdate: "notice", severity: "error", title: "Could not start agy", description: "Antigravity is not signed in on gigacomp." }
  await page.route("**/api/sessions/*/conversation**", (route) =>
    route.fulfill({ body: `${JSON.stringify({ seq: 1, ts: 1, kind: "notice", payload: notice })}\n`, contentType: "application/x-ndjson" }),
  )
  // A stopped chat has no worker, so the gateway has nothing to answer options with.
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ status: 404, body: "session unavailable" }))
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("Antigravity is not signed in on gigacomp.")).toBeVisible()
  await expect(page.getByText(/needs an update/)).toHaveCount(0)
})

test("a chat whose agent isn't signed in signs it in and then starts", async ({ page }) => {
  await page.clock.install()
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", harness: "agy", transport: "acp", ended_at: session.started_at, state: { ...session.state, running: false, exit_code: 1 } }] })
  })
  const notice = { sessionUpdate: "notice", severity: "error", title: "Could not start agy", description: "Antigravity is not signed in on gigacomp.", code: "auth_required", harness: "agy", methods: [{ id: "google", label: "Google account" }] }
  await page.route("**/api/sessions/*/conversation**", (route) =>
    route.fulfill({ body: `${JSON.stringify({ seq: 1, ts: 1, kind: "notice", payload: notice })}\n`, contentType: "application/x-ndjson" }),
  )
  const step = { id: "login-1", url: "https://accounts.google.com/o/oauth2/auth?x=1", input: "url", state: "waiting" }
  let started: unknown, pasted: unknown, resumed: unknown
  let signedIn = false
  await page.route("**/api/agents/agy/login", (route) => {
    started = route.request().postDataJSON()
    return route.fulfill({ json: step })
  })
  await page.route("**/api/agents/agy/login/login-1", (route) => {
    if (route.request().method() === "POST") {
      pasted = route.request().postDataJSON()
      signedIn = true
    }
    return route.fulfill({ json: { ...step, state: signedIn ? "done" : "waiting" } })
  })
  await page.route("**/api/sessions/chat-one/resume", (route) => {
    resumed = route.request().postDataJSON()
    return route.fulfill({ json: { ...session, id: "chat-one", harness: "agy", transport: "acp", conversation_state: "starting" } })
  })
  await page.goto("/sessions/local/chat-one")
  await page.getByRole("textbox", { name: "Message" }).fill("hello agy")
  const card = page.getByRole("group", { name: "Sign in to Antigravity" })
  await expect(card).toContainText("Antigravity is not signed in on gigacomp.")
  await card.getByRole("button", { name: "Sign in with Google account" }).click()
  expect(started).toEqual({ method: "google" })
  await expect(card.getByRole("link", { name: "Open sign-in page" })).toHaveAttribute("href", step.url)
  await card.getByLabel(/Paste the address the page lands on/).fill("https://antigravity.google/oauth-callback?code=abc")
  await card.getByRole("button", { name: "Continue" }).click()
  await expect.poll(() => pasted).toEqual({ input: "https://antigravity.google/oauth-callback?code=abc" })
  await page.clock.runFor(2500)
  await expect.poll(() => resumed).toEqual({ text: "hello agy" })
})

test("a message to a stopped chat resumes that chat", async ({ page }) => {
  const stopped = { ...session, id: "chat-one", transport: "acp", agent_session_id: "agent-1", ended_at: session.started_at, state: { ...session.state, running: false } }
  let resumed: unknown
  let release!: () => void
  const response = new Promise<void>((resolve) => { release = resolve })
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [resumed ? { ...stopped, ended_at: undefined, conversation_state: "starting" } : stopped] }))
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: "", contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/chat-one/resume", async (route) => {
    await response
    resumed = route.request().postDataJSON()
    return route.fulfill({ json: { ...stopped, ended_at: undefined, conversation_state: "starting" } })
  })
  await page.goto("/sessions/local/chat-one")
  const composer = page.getByRole("textbox", { name: "Message" })
  await composer.fill("continue")
  await page.keyboard.press("Enter")
  await expect(composer).toHaveValue("")
  await expect(page.getByText("continue", { exact: true })).toBeVisible()
  release()
  await expect.poll(() => resumed).toEqual({ text: "continue" })
  await expect(page).toHaveURL(/\/sessions\/local\/chat-one/)
  await expect(page.getByPlaceholder(/Starting/)).toBeVisible()
})

test("an Oh My Pi chat switches to a role's model and thinking from the model menu", async ({ page }) => {
  const posted: Record<string, unknown>[] = []
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({ json: [{ ...session, id: "chat-one", harness: "omp", transport: "acp" }] })
  })
  await page.route("**/api/sessions/*/conversation**", (route) => route.fulfill({ body: "", contentType: "application/x-ndjson" }))
  await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({
    json: {
      configOptions: [
        { type: "select", id: "model", name: "Model", category: "model", currentValue: "anthropic/opus", options: [{ value: "anthropic/opus", name: "Claude Opus" }, { value: "anthropic/haiku", name: "Claude Haiku" }] },
        { type: "select", id: "thinking", name: "Thinking", category: "thought_level", currentValue: "high", options: [{ value: "low", name: "Low" }, { value: "high", name: "High" }] },
      ],
    },
  }))
  await page.route("**/api/omp/settings", (route) => route.fulfill({ json: { version: "18.20.2", schema: [], values: {}, network: { "modelRoles.smol": "anthropic/haiku:low", "modelRoles.slow": "openai/gpt:xhigh" } } }))
  await page.route("**/api/sessions/*/conversation/config", (route) => {
    posted.push(route.request().postDataJSON() as Record<string, unknown>)
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  await page.getByRole("button", { name: "Model and effort" }).click()
  await expect(page.getByRole("menuitem", { name: /Thinking/ })).toHaveCount(0)
  await page.getByRole("menuitem", { name: /^Fast/ }).click()
  await expect.poll(() => posted).toEqual([{ key: "model", value: "anthropic/haiku" }, { key: "thinking", value: "low" }])
})

test("a hidden chat reconnects from the last sequence it received", async ({ page }) => {
  const cursors: string[] = []
  const entry = (seq: number, text: string) => JSON.stringify({
    seq,
    ts: seq,
    kind: "agent_message_chunk",
    payload: { text: { type: "text", text } },
  })
  await page.addInitScript(() => {
    let visibility = "visible"
    Object.defineProperty(document, "visibilityState", { get: () => visibility })
    document.addEventListener("test-hide", () => {
      visibility = "hidden"
      document.dispatchEvent(new Event("visibilitychange"))
    })
    document.addEventListener("test-show", () => {
      visibility = "visible"
      document.dispatchEvent(new Event("visibilitychange"))
    })
  })
  await page.route("**/api/sessions/chat-one/conversation**", (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith("/config-options")) return route.fulfill({ json: [] })
    if (!url.searchParams.has("stream")) return route.fulfill({
      body: entry(1, "Initial message") + "\n",
      contentType: "application/x-ndjson",
    })
    cursors.push(url.searchParams.get("since") ?? "")
    if (cursors.length > 1) return route.abort()
    return route.fulfill({
      body: `id: 2\ndata: ${entry(2, "Streamed message")}\n\n`,
      contentType: "text/event-stream",
    })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText(/Streamed message/)).toBeVisible()
  await page.evaluate(() => {
    document.dispatchEvent(new Event("test-hide"))
    document.dispatchEvent(new Event("test-show"))
  })
  await expect.poll(() => cursors).toEqual(["1", "2"])
})

test("the offline cache writes changed queries and skips a streaming chat", async ({ page }) => {
  await page.route("**/api/sessions/chat-one/conversation**", (route) => {
    const url = new URL(route.request().url())
    if (!url.searchParams.has("stream")) return route.fallback()
    return route.fulfill({ body: ": ping\n\n", contentType: "text/event-stream" })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("I'll update the reattach path")).toBeVisible()
  const readCache = () => page.evaluate(() => new Promise<Array<{ key: unknown[]; at: number }>>((resolve, reject) => {
    const opening = indexedDB.open("agentpack-cache", 2)
    opening.onerror = () => reject(opening.error)
    opening.onsuccess = () => {
      const db = opening.result
      const tx = db.transaction("entries")
      const request = tx.objectStore("entries").getAll()
      tx.oncomplete = () => {
        db.close()
        resolve(request.result.map((row) => ({ key: row.query.queryKey, at: row.query.dehydratedAt })))
      }
      tx.onerror = () => reject(tx.error)
    }
  }))
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")))
  await expect.poll(async () => (await readCache()).some(({ key }) => key.includes("sessions"))).toBe(true)
  expect((await readCache()).filter(({ key }) => key[0] === "conversation")).toEqual([])
  const entries = await readCache()
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { value: "hidden" })
    document.dispatchEvent(new Event("visibilitychange"))
  })
  await expect.poll(async () => (await readCache()).filter(({ key }) => key[0] === "conversation").length).toBe(1)
  const status = (rows: typeof entries) => rows.find(({ key }) => key[0] === "status")
  expect(status(entries)).toBeDefined()
  expect(status(await readCache())).toEqual(status(entries))
})

test("a live conversation uses the shared events connection", async ({ page }) => {
  const cursors: string[] = []
  let perChatStreams = 0
  await page.route("**/api/config", (route) => route.fulfill({ json: { conversation_events: true, ui_build_id: "multiplexed" } }))
  await page.route("**/api/sessions/chat-one/conversation**", (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith("/config-options")) return route.fulfill({ json: [] })
    if (url.searchParams.has("stream")) {
      perChatStreams++
      return route.abort()
    }
    return route.fulfill({
      body: '{"seq":5,"ts":1,"kind":"agent_message_chunk","payload":{"text":{"text":"Local snapshot"}}}\n',
      contentType: "application/x-ndjson",
    })
  })
  await page.route("**/api/events?*", (route) => {
    const url = new URL(route.request().url())
    if (url.searchParams.get("session") !== "chat-one") return route.fallback()
    cursors.push(url.searchParams.get("since") ?? "")
    return route.fulfill({
      body: 'event: conversation\nid: chat-one:6\ndata: {"seq":6,"ts":2,"kind":"agent_message_chunk","payload":{"text":{"text":" and live tail"}}}\n\n',
      contentType: "text/event-stream",
    })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("Local snapshot and live tail")).toBeVisible()
  expect(cursors[0]).toBe("5")
  expect(perChatStreams).toBe(0)
})

test("a long conversation starts at the latest turn and renders only nearby rows", async ({ page }) => {
  await page.route("**/api/sessions", (route) => route.fulfill({
    json: [{ ...session, id: "chat-one", transport: "acp", state: { ...session.state, running: false } }],
  }))
  const longChat = Array.from({ length: 120 }, (_, index) => [
    { seq: index * 3 + 1, ts: index * 3, kind: "prompt", payload: { text: { text: `Question ${index}` } } },
    { seq: index * 3 + 2, ts: index * 3 + 1, kind: "agent_message_chunk", payload: { text: { text: `Reply number ${index}` } } },
    { seq: index * 3 + 3, ts: index * 3 + 2, kind: "turn_end", payload: {} },
  ]).flat().map((entry) => JSON.stringify(entry)).join("\n")
  await page.route("**/api/sessions/chat-one/conversation", (route) => route.fulfill({
    body: longChat, contentType: "application/x-ndjson",
  }))
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByText("Reply number 119", { exact: true })).toBeInViewport()
  expect(await page.locator("[data-thread-virtual] > [data-index]").count()).toBeLessThan(50)
  await page.getByLabel("Conversation", { exact: true }).evaluate((element) => { element.scrollTop = 0 })
  await expect(page.getByText("Question 0", { exact: true })).toBeInViewport()
})
