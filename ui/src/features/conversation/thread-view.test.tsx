import { act, createElement } from "react"
import { createRoot, type Root } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ThreadItem } from "./model"
import { findPendingPermission, latestUserPrompt, quoteMessage, ThreadView } from "./thread-view"

const items: ThreadItem[] = [
  { kind: "user_message_chunk", data: { text: { text: "Run the tests" } } },
  { kind: "agent_message_chunk", data: { text: { text: "The tests pass." } } },
]

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  container = document.createElement("div")
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe("conversation message actions", () => {
  it("quotes lines and selects the latest user prompt", () => {
    expect(quoteMessage("first line\nsecond line")).toBe("> first line\n> second line")
    expect(latestUserPrompt(items)?.text?.text).toBe("Run the tests")
  })

  it("wires retry and fork actions, omitting them when unavailable", () => {
    const retry = vi.fn()
    const fork = vi.fn()
    act(() => root.render(createElement(ThreadView, {
      items,
      machine: "local",
      sessionId: "session-1",
      onRetry: retry,
      onFork: fork,
    })))

    act(() => container.querySelector<HTMLButtonElement>('[aria-label="Retry last message"]')!.click())
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="Fork conversation"]')!.click())
    expect(retry).toHaveBeenCalledOnce()
    expect(fork).toHaveBeenCalledOnce()

    act(() => root.render(createElement(ThreadView, {
      items: [items[1]!],
      machine: "local",
      sessionId: "session-1",
      onRetry: retry,
    })))
    expect(container.querySelector('[aria-label="Retry last message"]')).toBeNull()
    expect(container.querySelector('[aria-label="Fork conversation"]')).toBeNull()
  })
  it("renders every recorded attachment from its session-scoped blob", () => {
    act(() => root.render(createElement(ThreadView, {
      items: [{
        kind: "user_message_chunk",
        data: {
          images: [
            { blob: "sha256:one", name: "one.png", kind: "image" },
            { blob: "sha256:two", name: "two.webp", kind: "resource_link" },
          ],
        },
      }],
      machine: "local",
      sessionId: "session-1",
    })))
    expect(container.querySelectorAll('[data-slot="user-attachments"] button')).toHaveLength(2)
    expect([...container.querySelectorAll<HTMLImageElement>('[data-slot="user-attachments"] img')].map((image) => image.getAttribute("src"))).toEqual([
      "/api/sessions/session-1/blobs/one",
      "/api/sessions/session-1/blobs/two",
    ])
  })

  it("groups a turn's tools into one summary, opened by a failure", () => {
    const tool = (seq: number, ts: number, status: string, extra = {}): ThreadItem => ({ kind: "tool", seq, ts, endTs: ts + 1000, data: { title: `t${seq}`, kind: "read", status, ...extra } })
    act(() => root.render(createElement(ThreadView, {
      items: [
        tool(1, 0, "completed"),
        tool(2, 2000, "failed"),
        tool(3, 4000, "completed", { kind: "edit", content: [{ type: "diff", path: "a.go", oldText: "x", newText: "y" }] }),
        { kind: "agent_message_chunk", seq: 4, data: { text: { text: "Done" } } },
      ],
      machine: "local",
      sessionId: "s",
    })))
    const group = container.querySelector('[data-slot="tool-group"]')!
    expect(group.querySelector("summary")!.textContent).toBe("Worked for 5 s · 3 tools · 1 file changed1 failed")
    expect(group.hasAttribute("open")).toBe(true)
    expect(container.querySelectorAll('[data-slot="tool-group"]')).toHaveLength(1)
  })

  it("shows tool input on one line once the row opens", () => {
    act(() => root.render(createElement(ThreadView, {
      items: [{ kind: "tool", data: { title: "Search", kind: "search", status: "completed", rawInput: { pattern: "replay" } } }],
      machine: "local",
      sessionId: "s",
    })))
    // A closed row mounts no body; opening it shows the input.
    const row = container.querySelector<HTMLDetailsElement>("[data-tool-row]")!
    expect(container.querySelector('[data-slot="tool-input"]')).toBeNull()
    act(() => {
      row.open = true
      row.dispatchEvent(new Event("toggle"))
    })
    expect(container.querySelector('[data-slot="tool-input"]')!.textContent).toBe("pattern: replay")
  })

  it("shows the pending permission inline and finds the newest one", () => {
    const perm = (requestId: string, resolved?: boolean): ThreadItem => ({ kind: "permission", data: { requestId, tool: "Bash", resolved } })
    const list = [perm("a"), perm("b"), perm("c", true)]
    expect(findPendingPermission(list)).toBe(list[1])
    expect(findPendingPermission([list[2]!])).toBeUndefined()
    act(() => root.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: list, machine: "local", sessionId: "s", agent: "Claude" }))))
    expect(container.querySelectorAll('[role="group"][data-slot="permission-card"]')).toHaveLength(2)
    expect(container.textContent).toContain("Allow Claude to run this command?")
  })
})
