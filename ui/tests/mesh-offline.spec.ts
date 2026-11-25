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
  await page.screenshot({path: "/tmp/agentpack-mesh-offline-desktop.png"})
  await page.setViewportSize({width:390,height:844})
  await expect(page.getByText("Saved while online", { exact: true })).toBeVisible()
  await page.screenshot({path: "/tmp/agentpack-mesh-offline-phone.png"})
  await page.reload()
  await expect(page.getByText("Saved while online", { exact: true })).toBeVisible()
  await expect(page.getByText(/Offline · saved history · Last seen/)).toBeVisible()
  expect(writes).toEqual([])
  await page.goto("/machines")
  await expect(page.getByRole("navigation",{name:"Machines"}).getByText("Main",{exact:true})).toBeVisible()
  await page.screenshot({path:"/tmp/agentpack-main-badge-phone.png"})
  await page.setViewportSize({width:1280,height:720})
  await page.goto("/machines")
  await expect(page.getByRole("navigation",{name:"Machines"}).getByText("Main",{exact:true})).toBeVisible()
  await page.screenshot({path:"/tmp/agentpack-main-badge-desktop.png"})
})

test("legacy Library peers require explicit full mesh trust", async ({page}) => {
  await mockAPI(page)
  let enrollments=0
  await page.route("**/api/nodes*",route=>route.fulfill({json:[{id:"legacy",name:"Old computer",online:true}]}))
  await page.route("**/api/library/replication/peers",route=> {
    if(route.request().method()==="POST"){enrollments++;return route.fulfill({json:{enrolled:true}})}
    return route.fulfill({json:{peers:[{node:"legacy",receipt_count:3,digests:[],mesh:enrollments>0}],contacts:{legacy:{at:"2026-11-14T10:00:00Z",last_sync:"2026-11-14T10:00:00Z",schema_version:12,lag:0,version:4}}}})
  })
  await page.goto("/settings/replication")
  await page.getByRole("button",{name:"Enable mesh sync"}).click()
  await expect(page.getByText(/password hashes, chat history/)).toBeVisible()
  expect(enrollments).toBe(0)
  await page.getByRole("button",{name:"Trust and enroll"}).click()
  await expect.poll(()=>enrollments).toBe(1)
  await expect(page.getByRole("button",{name:"Sync now"})).toBeVisible()
  await expect(page.getByText("Stale · schema 12")).toBeVisible()
})
