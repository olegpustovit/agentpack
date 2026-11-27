import { expect, test } from "@playwright/test"
import { mockAPI, session } from "./helpers"

test("replicated offline chat survives reload, keeps history and refuses all inputs", async ({ page }) => {
  await mockAPI(page)
  const reads: string[] = []
  const writes: string[] = []
  await page.route("**/api/nodes*", (route) => route.fulfill({ json: [{ id: "asleep", name: "Sleeping computer", online: false, main: true }] }))
  await page.route("**/api/sessions", (route) => {
    reads.push(route.request().url())
    return route.fulfill({ json: [{ ...session, id: "saved-chat", node_id: "asleep", reachable: false, last_sync: "2026-11-14T10:00:00Z", transport: "acp" }] })
  })
  await page.route("**/api/sessions/*/conversation**", (route) => {
    if (route.request().method() !== "GET") writes.push(route.request().url())
    return route.fulfill({ contentType: "application/x-ndjson", body: [
      {seq: 1, kind: "agent_message_chunk", payload: {text: {text: "Saved while online"}}},
      {seq: 2, kind: "permission_request", payload: {requestId: "p1", tool: "Read file", options: [{id: "allow", label: "Allow once"}]}},
    ].map((row) => JSON.stringify(row)).join("\n") })
  })
  await page.goto("/sessions/asleep/saved-chat")
  await expect(page.getByText("Saved while online", { exact: true })).toBeVisible()
  await expect(page.getByText(/Offline · saved history · Last seen/)).toBeVisible()
  await expect(page.getByRole("textbox", {name: "Message", exact: true})).toHaveCount(0)
  await expect(page.getByRole("button", { name: /Allow once/ })).toHaveCount(0)
  await expect(page.getByText("Saved request · read-only")).toBeVisible()
  await page.keyboard.press("1")
  expect(writes).toEqual([])
  expect(reads).toHaveLength(1)
  expect(new URL(reads[0]!).search).toBe("")
  await page.reload()
  await expect(page.getByText("Saved while online", { exact: true })).toBeVisible()
  await expect(page.getByText(/Offline · saved history · Last seen/)).toBeVisible()
  expect(writes).toEqual([])
})

test("legacy Library peers require explicit mesh approval", async ({ page }) => {
  await mockAPI(page)
  const approvals: unknown[] = []
  await page.route("**/api/nodes*", (route) => route.fulfill({
    json: [{ id: "legacy", name: "Old computer", online: true }],
  }))
  await page.route("**/api/library/replication/peers", (route) => {
    if (route.request().method() === "POST") {
      approvals.push(route.request().postDataJSON())
      return route.fulfill({ json: { enrolled: true } })
    }
    return route.fulfill({ json: { peers: [{ node: "legacy", mesh: approvals.length > 0 }], contacts: {} } })
  })
  await page.goto("/machines/topology")
  await expect(page.getByText("Waiting for your approval")).toBeVisible()
  await page.getByRole("button", { name: "Approve" }).click()
  const dialog = page.getByRole("alertdialog", { name: "Add Old computer to this mesh?" })
  expect(approvals).toEqual([])
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(page.getByRole("button", { name: "Old computer actions" })).toBeVisible()
  expect(approvals).toEqual([{ peer: "legacy" }])
})
