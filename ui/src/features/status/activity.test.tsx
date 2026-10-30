import { QueryClientProvider, type QueryClient } from "@tanstack/react-query"
import { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it } from "vitest"
import { createQueryClient } from "@/lib/query-client"
import { ActivityItem } from "./activity"

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

let container: HTMLDivElement
let client: QueryClient
let root: Root

beforeEach(async () => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement("div")
  document.body.appendChild(container)
  client = createQueryClient()
  root = createRoot(container)
  await act(async () => root.render(<QueryClientProvider client={client}><ActivityItem /></QueryClientProvider>))
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  client.clear()
})

describe("ActivityItem", () => {
  it("shows nothing at rest and appears only after 400 ms of machine fetching", async () => {
    expect(container.textContent).toBe("")
    // client.clear() in teardown cancels this fetch; the cancellation is expected.
    client
      .fetchQuery({ queryKey: ["m", "box", "sessions"], queryFn: () => new Promise(() => {}), retry: false })
      .catch(() => {})
    await act(async () => wait(100))
    expect(container.textContent).toBe("")
    await act(async () => wait(400))
    expect(container.textContent).toContain("Updating 1 machine")
  })

  it("never shows for a fetch that finishes inside the delay", async () => {
    await act(async () => client.fetchQuery({ queryKey: ["m", "box", "sessions"], queryFn: () => 1 }))
    await act(async () => wait(500))
    expect(container.textContent).toBe("")
  })
})
