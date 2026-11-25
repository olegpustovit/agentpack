import { act, createElement } from "react"
import { createRoot, type Root } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
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

  it("names the chat that sent a message and hides the header its agent reads", () => {
    act(() => root.render(createElement(ThreadView, {
      items: [
        { kind: "user_message_chunk", data: { from: { session: "session-a", name: "Refactor auth", task: "t_1", kind: "ask" }, text: { text: "[agentpack message · task t_1]\nAnswer with reply.\n---\nReview auth.go" } } },
        { kind: "user_message_chunk", data: { from: { session: "session-b", task: "t_2", kind: "reply", state: "input_required" }, text: { text: "[agentpack question]\n---\nWhich file?" } } },
      ],
      machine: "local",
      sessionId: "session-1",
    })))
    expect([...container.querySelectorAll('[data-slot="user-bubble"]')].map((bubble) => bubble.textContent)).toEqual(["Review auth.go"])
    // A reply or question is a card with the answer rendered as agent markdown.
    expect(container.querySelector('[data-slot="reply-card"]')?.textContent).toContain("Which file?")
    expect(container.textContent).toContain("Message from Refactor auth")
    expect(container.textContent).toContain("Question from another chat")
  })

  it("opens the chat that sent a message", () => {
    const open = vi.fn()
    const from = { session: "session-a", node: "node-2", name: "Refactor auth", task: "t_1", kind: "ask" as const }
    act(() => root.render(createElement(ThreadView, {
      items: [{ kind: "user_message_chunk", data: { from, text: { text: "[h]\n---\nhi" } } }],
      machine: "local",
      sessionId: "session-1",
      onOpenChat: open,
    })))
    act(() => container.querySelector<HTMLButtonElement>('button[aria-label="Open Refactor auth"]')!.click())
    expect(open).toHaveBeenCalledWith(from)
  })

  it("groups a run of thoughts and tools into one row titled by the latest thought", () => {
    const tool = (seq: number, ts: number, status: string, extra = {}): ThreadItem => ({ kind: "tool", seq, ts, endTs: ts + 1000, data: { title: `t${seq}`, kind: "read", status, ...extra } })
    act(() => root.render(createElement(ThreadView, {
      items: [
        tool(1, 0, "completed"),
        { kind: "agent_thought_chunk", seq: 10, ts: 1500, data: { text: { text: "**Checking logs**" } } },
        tool(2, 2000, "failed"),
        tool(3, 4000, "completed", { kind: "edit", content: [{ type: "diff", path: "a.go", oldText: "x", newText: "y" }] }),
        { kind: "agent_message_chunk", seq: 4, data: { text: { text: "Done" } } },
      ],
      machine: "local",
      sessionId: "s",
    })))
    const group = container.querySelector('[data-slot="tool-group"]')!
    expect(group.querySelector("summary")!.textContent).toBe("Checking logs · 2 reads · 1 file changed · 5s · 1 failed")
    expect(group.hasAttribute("open")).toBe(false)
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

  it("keeps agent and user message text selectable inside the context menu trigger", () => {
    act(() => root.render(createElement(ThreadView, {
      items,
      machine: "local",
      sessionId: "session-1",
    })))
    const triggers = [...container.querySelectorAll<HTMLElement>('[data-slot="context-menu-trigger"]')]
    expect(triggers).toHaveLength(2)
    for (const trigger of triggers) {
      expect(trigger.classList.contains("select-none")).toBe(false)
      expect(trigger.classList.contains("select-text")).toBe(true)
    }
  })

  it("shows the pending permission inline", () => {
    const perm = (requestId: string, resolved?: boolean): ThreadItem => ({ kind: "permission", data: { requestId, tool: "Bash", resolved } })
    const list = [perm("a"), perm("b"), perm("c", true)]
    act(() => root.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: list, machine: "local", sessionId: "s", agent: "Claude" }))))
    expect(container.querySelectorAll('[role="group"][data-slot="permission-card"]')).toHaveLength(2)
    expect(container.textContent).toContain("Allow Claude to run this command?")
    // Pinned above the composer instead, so the thread only marks where they were asked.
    act(() => root.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: list, machine: "local", sessionId: "s", pinRequests: true }))))
    expect(container.querySelectorAll('[role="group"][data-slot="permission-card"]')).toHaveLength(0)
    expect(container.querySelectorAll('[data-slot="pinned-request-marker"]')).toHaveLength(2)
  })

  it("retires the sign-in card once the agent answers after it", () => {
    vi.stubGlobal("fetch", vi.fn(async () => Response.json([])))
    const notice: ThreadItem = { kind: "divider", data: { title: "Failed to authenticate", code: "auth_required", harness: "claude", methods: [{ id: "claude", label: "Claude account" }] } }
    const view = (list: ThreadItem[]) => createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: list, machine: "local", sessionId: "s", onSignedIn: () => {} }))
    act(() => root.render(view([items[0]!, notice])))
    expect(container.querySelector('[data-slot="signin-card"]')).not.toBeNull()
    act(() => root.render(view([items[0]!, notice, ...items])))
    expect(container.querySelector('[data-slot="signin-card"]')).toBeNull()
    vi.unstubAllGlobals()
  })

  it("offers a URL request's link with Done and Decline, and answers it", async () => {
    const posted: unknown[] = []
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
      posted.push(JSON.parse(init.body as string))
      return Response.json({ accepted: true })
    }))
    const item: ThreadItem = { kind: "elicitation", data: { elicitationId: "e1", agentElicitationId: "codex_apps_auth_1", prompt: "Connect Artifacts", url: "https://chatgpt.com/apps/artifacts/x" } }
    act(() => root.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: [item], machine: "local", sessionId: "s" }))))
    const link = container.querySelector<HTMLAnchorElement>('[data-slot="elicitation-card"] a')!
    expect(link.href).toBe("https://chatgpt.com/apps/artifacts/x")
    expect(link.textContent).toBe("Open chatgpt.com")
    const decline = [...container.querySelectorAll("button")].find((button) => button.textContent === "Decline")!
    await act(async () => decline.click())
    expect(posted).toEqual([{ elicitation_id: "e1", value: "decline" }])
    vi.unstubAllGlobals()
  })

  it("submits question forms and multi-choice cards on Cmd/Ctrl+Enter", async () => {
    const posted: { elicitation_id: string; value: string }[] = []
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
      posted.push(JSON.parse(init.body as string) as { elicitation_id: string; value: string })
      return Response.json({ accepted: true })
    }))
    const formItem: ThreadItem = {
      kind: "elicitation",
      seq: 1,
      data: {
        elicitationId: "e-form",
        prompt: "Free the display?",
        fields: [
          { key: "choice", title: "Status", options: [{ value: "free", label: "Free now" }, { value: "busy", label: "Busy" }] },
          { key: "note", title: "Additional answer or note" },
        ],
      },
    }
    act(() => root.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: [formItem], machine: "local", sessionId: "s" }))))
    const freeButton = [...container.querySelectorAll("button")].find((button) => button.textContent === "Free now")!
    await act(async () => freeButton.click())
    const input = container.querySelector<HTMLInputElement>('input[aria-label="Additional answer or note"]')!
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!
    await act(async () => {
      setter.call(input, "should be free")
      input.dispatchEvent(new Event("input", { bubbles: true }))
    })
    // IME composition must not trigger submit.
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, isComposing: true, bubbles: true, cancelable: true }))
    })
    expect(posted).toEqual([])
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }))
    })
    expect(posted).toEqual([{ elicitation_id: "e-form", value: JSON.stringify({ choice: "free", note: "should be free" }) }])

    const multiItem: ThreadItem = {
      kind: "elicitation",
      seq: 2,
      data: {
        elicitationId: "e-multi",
        prompt: "Pick targets",
        choices: ["Local", "Remote"],
        optionIds: ["local-id", "remote-id"],
        allowMultiple: true,
      },
    }
    act(() => root.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: [multiItem], machine: "local", sessionId: "s" }))))
    const localButton = [...container.querySelectorAll("button")].find((button) => button.textContent === "Local")!
    // Nothing selected yet: Ctrl+Enter does not submit.
    await act(async () => {
      localButton.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true, cancelable: true }))
    })
    expect(posted).toHaveLength(1)
    await act(async () => localButton.click())
    await act(async () => {
      localButton.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true, cancelable: true }))
    })
    expect(posted[1]).toEqual({ elicitation_id: "e-multi", value: JSON.stringify(["local-id"]) })
    vi.unstubAllGlobals()
  })
})

describe("tool row titles (P1.34)", () => {
  it("names an orphan update by the agent's tool name, its input, then its kind, never a bare Tool", () => {
    const tool = (data: object): ThreadItem => ({ kind: "tool", data: { toolCallId: "t", status: "completed", ...data } })
    // One at a time: adjacent tools collapse into a group.
    for (const [data, name] of [[{ _meta: { claudeCode: { toolName: "Grep" } } }, "Grep"], [{ rawInput: { description: "List the worktrees" } }, "List the worktrees"], [{ kind: "fetch" }, "fetch"]] as const) {
      act(() => root.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(ThreadView, { items: [tool(data)], machine: "local", sessionId: "s", agent: "Claude" }))))
      expect(container.textContent).toContain(name)
      expect(container.textContent).not.toMatch(/\bTool\b/)
    }
  })
})
