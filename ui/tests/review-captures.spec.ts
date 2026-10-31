import { mkdirSync } from "node:fs"
import { expect, test, type Locator, type Page } from "@playwright/test"
import { mockAPI } from "./helpers"

// Writes review screenshots only; no pixel assertions. Runs once, on desktop Chromium.
test.skip(({ browserName, isMobile }) => browserName !== "chromium" || isMobile, "captures run on the desktop chromium project only")

const dir = "/tmp/t71-screens"
const views: { name: string; path: string; ready: (page: Page) => Locator }[] = [
  { name: "sessions", path: "/sessions", ready: (page) => page.getByRole("navigation", { name: "Sessions" }) },
  { name: "library", path: "/library", ready: (page) => page.getByRole("navigation", { name: "Library" }) },
  { name: "machines", path: "/machines/peer-1", ready: (page) => page.getByRole("heading", { level: 1, name: "Build server" }) },
  { name: "topology", path: "/machines/topology", ready: (page) => page.getByRole("heading", { level: 1, name: "Topology" }) },
  { name: "limits", path: "/limits", ready: (page) => page.getByRole("table", { name: "Provider limits" }) },
  { name: "settings", path: "/settings/general", ready: (page) => page.getByRole("navigation", { name: "Settings sections" }) },
]
const widths = [1440, 1024, 390]
const themes = ["light", "dark"] as const

for (const view of views) {
  for (const width of widths) {
    for (const theme of themes) {
      test(`capture ${view.name} ${width} ${theme}`, async ({ page }) => {
        await mockAPI(page, { fleet: true })
        // The shared mock answers unknown endpoints with {}; the library list maps over drafts.
        await page.route("**/api/library/drafts", (route) => route.fulfill({ json: [] }))
        await page.setViewportSize({ width, height: 900 })
        await page.emulateMedia({ colorScheme: theme })
        await page.goto(view.path)
        await expect(view.ready(page)).toBeVisible()
        await page.waitForLoadState("networkidle")
        await expect(page.getByRole("status", { name: "Loading" })).toHaveCount(0)
        mkdirSync(dir, { recursive: true })
        await page.screenshot({ path: `${dir}/${view.name}-${width}-${theme}.png` })
      })
    }
  }
}
