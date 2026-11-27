import { expect, test } from "@playwright/test"
import { identity, mockAPI, session } from "./helpers"

test("History searches older pages up to the cap and can load more", async ({ page }) => {
  await mockAPI(page)
  await page.route("**/api/sessions?history=1*", async (route) => {
    const query = new URL(route.request().url()).searchParams
    expect(query.has("node")).toBe(false)
    const start = Number(query.get("before")?.split(":")[0] ?? 0)
    const end = Math.min(start + 100, 1001)
    const sessions = Array.from({ length: end - start }, (_, index) => {
      const id = start + index + 1
      return {
        ...session,
        id: `chat-${id}`,
        node_id: identity.node.id,
        transport: "acp",
        name: id === 1001 ? "Older match" : "Another chat",
      }
    })
    await route.fulfill({ json: { sessions, next: end === 1001 ? "" : `${end}:chat-${end}` } })
  })
  await page.goto("/sessions?view=history&find=Older")
  await expect(page.getByText("Showing the newest 1000; scroll for older")).toBeVisible()
  await page.getByRole("button", { name: "Show older" }).click()
  await expect(page.getByRole("link", { name: /Older match/ })).toHaveAttribute("href", "/sessions/local/chat-1001")
})
