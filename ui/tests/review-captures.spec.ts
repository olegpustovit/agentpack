import { mkdirSync, readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"
import { expect, test, type Locator, type Page } from "@playwright/test"
import { conversationImage, mockAPI, session } from "./helpers"

const conversationFixture = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "../../testdata/acp/full.jsonl"),
  "utf8",
)

// Writes review screenshots only; no pixel assertions. Runs once, on desktop Chromium.
test.skip(({ browserName, isMobile }) => browserName !== "chromium" || isMobile, "captures run on the desktop chromium project only")

const dir = "/tmp/t71-screens"
const views: { name: string; path: string; ready: (page: Page) => Locator }[] = [
  { name: "sessions", path: "/sessions", ready: (page) => page.getByRole("navigation", { name: "Sessions" }) },
  { name: "conversation", path: "/sessions/local/local-one", ready: (page) => page.locator('[data-slot="agent-message"]').first() },
  { name: "library", path: "/library", ready: (page) => page.getByRole("navigation", { name: "Library" }) },
  { name: "machines", path: "/machines/peer-1", ready: (page) => page.getByRole("heading", { level: 1, name: "Build server" }) },
  { name: "topology", path: "/machines/topology", ready: (page) => page.getByRole("heading", { level: 1, name: "Topology" }) },
  { name: "limits", path: "/limits", ready: (page) => page.locator("[data-provider]").first() },
  { name: "settings", path: "/settings/general", ready: (page) => page.getByRole("navigation", { name: "Settings sections" }) },
]
const widths = [1440, 1024, 390]
const themes = ["light", "dark"] as const

for (const view of views) {
  for (const width of widths) {
    for (const theme of themes) {
      test(`capture ${view.name} ${width} ${theme}`, async ({ page }) => {
        await mockAPI(page, { fleet: view.name !== "conversation" })
        // The shared mock answers unknown endpoints with {}; the library list maps over drafts.
        await page.route("**/api/library/drafts", (route) => route.fulfill({ json: [] }))
        if (view.name === "conversation") {
          await page.route("**/api/sessions", (route) => {
            if (route.request().method() !== "GET") return route.fallback()
            return route.fulfill({ json: [{ ...session, transport: "acp" }] })
          })
          await page.route("**/api/sessions/*/conversation**", (route) =>
            route.fulfill({ body: conversationFixture, contentType: "application/x-ndjson" }),
          )
          await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [
            { type: "select", id: "mode", name: "Permissions", currentValue: "manual", options: [{ value: "manual", name: "Manual" }, { value: "acceptEdits", name: "Accept edits" }, { value: "plan", name: "Plan" }, { value: "bypass", name: "Bypass" }] },
            { type: "select", id: "model", name: "Model", currentValue: "opus", options: [{ value: "opus", name: "Opus 5.5" }, { value: "sonnet", name: "Sonnet 5.5" }, { value: "haiku", name: "Haiku 5.5" }] },
            { type: "select", id: "effort", name: "Effort", currentValue: "high", options: [{ value: "low", name: "Low" }, { value: "medium", name: "Medium" }, { value: "high", name: "High" }] },
          ] }))
          await page.route("**/api/sessions/*/blobs/**", (route) =>
            route.fulfill({ body: conversationImage, contentType: "image/png" }),
          )
        }
        await page.setViewportSize({ width, height: 900 })
        await page.emulateMedia({ colorScheme: theme })
        await page.goto(view.path)
        await expect(view.ready(page)).toBeVisible()
        await page.waitForLoadState("networkidle")
        if (view.name !== "conversation") await expect(page.getByRole("status", { name: "Loading" })).toHaveCount(0)
        mkdirSync(dir, { recursive: true })
        await page.screenshot({ path: `${dir}/${view.name}-${width}-${theme}.png` })
      })
    }
  }
}

