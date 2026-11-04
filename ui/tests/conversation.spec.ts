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
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
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
  await expect(inspector.getByText("Agent capabilities")).toBeVisible()
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
  const toggle = page.getByRole("switch", { name: "Verbose output" })
  await expect(toggle).not.toBeChecked()
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

test("permission shortcut works with no control focused", async ({ page }) => {
  let posted: Record<string, unknown> | undefined
  await page.route("**/api/sessions/*/conversation/permission", (route) => {
    posted = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { accepted: true } })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.locator('[data-slot="permission-card"]').first()).toBeVisible()
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
    document.body.focus()
  })
  await page.keyboard.press("1")
  await expect.poll(() => posted).toBeDefined()
  expect(posted).toMatchObject({ request_id: "p1", option_id: "allow" })
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

test("exploration starts collapsed and leaves diff and terminal details open", async ({ page }) => {
  await page.goto("/sessions/local/chat-one")
  const group = page.locator('[data-slot="tool-group"]')
  await expect(group).not.toHaveAttribute("open")
  await group.locator("summary").click()
  await expect(group.locator("li")).toHaveCount(4)
  await expect(group.locator("details")).toHaveCount(0)
  await expect(page.locator('[data-slot="tool-diff-block"]')).toHaveAttribute("open", "")
  await expect(page.locator('[data-slot="tool-terminal-block"]')).toHaveAttribute("open", "")
})

test("terminal tool block shows command output", async ({ page }) => {
  await page.goto("/sessions/local/chat-one")
  await expect(page.locator('[data-slot="tool-terminal-block"]')).toHaveAttribute("open", "")
  await expect(page.locator('[data-slot="tool-terminal"]')).toContainText("ok")
})

test("working acp session disables terminal view switch", async ({ page }) => {
  await page.route("**/api/sessions", (route) => route.request().method() === "GET"
    ? route.fulfill({ json: [{ ...session, id: "chat-one", transport: "acp", conversation_state: "working" }] })
    : route.fallback())
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByRole("button", { name: "Terminal", exact: true })).toBeDisabled()
})

test("idle acp session can switch while its worker remains running", async ({ page }) => {
  let posted: unknown
  await page.route("**/api/sessions", (route) => {
    if (route.request().method() === "POST") {
      posted = route.request().postDataJSON()
      return route.fulfill({ json: { ...session, id: "term-one", transport: "terminal", state: { ...session.state, running: true } } })
    }
    if (route.request().method() !== "GET") return route.fallback()
    return route.fulfill({
      json: [{ ...session, id: "chat-one", transport: "acp", conversation_state: "idle", state: { ...session.state, running: true } }],
    })
  })
  await page.goto("/sessions/local/chat-one")
  await expect(page.getByRole("button", { name: "Terminal", exact: true })).toBeEnabled()
  await page.getByRole("button", { name: "Terminal", exact: true }).click()
  await expect(page).toHaveURL(/term-one/)
  expect(posted).toMatchObject({ transport: "terminal" })
})

test("stopped acp session can POST a terminal session on view switch", async ({ page }) => {
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
  await page.getByRole("button", { name: "Terminal", exact: true }).click()
  await expect(page).toHaveURL(/term-one/)
  expect(posted).toMatchObject({ transport: "terminal", directory: session.directory })
})

test("permission answer refetches conversation log", async ({ page }) => {
  let fetches = 0
  await page.route("**/api/sessions/*/conversation**", (route) => {
    if (route.request().url().includes("/permission")) return route.fallback()
    fetches += 1
    return route.fulfill({ body: fixtureBody, contentType: "application/x-ndjson" })
  })
  await page.route("**/api/sessions/*/conversation/permission", (route) => route.fulfill({ json: { accepted: true } }))
  await page.goto("/sessions/local/chat-one")
  await expect.poll(() => fetches).toBeGreaterThan(0)
  const before = fetches
  await page.getByRole("button", { name: "Allow once" }).click()
  await expect.poll(() => fetches).toBeGreaterThan(before)
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
