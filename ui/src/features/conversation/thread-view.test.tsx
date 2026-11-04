import { act, createElement } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ThreadItem } from "./model"
import { latestUserPrompt, quoteMessage, ThreadView } from "./thread-view"

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
})
