import { act } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it } from "vitest"
import type { SessionInfo } from "@/lib/api-types"
import type { ThreadState } from "./model"
import { ConversationInspector } from "./conversation-inspector"

const thread: ThreadState = { items: [], needs_you: false, working: false }
const info: SessionInfo = {
  id: "session-1",
  directory: "/work/project",
  harness: "claude",
  mode: "default",
  state: { interactive: false, running: true, restarting: false, rows: 40, columns: 120, exit_code: 0 },
  started_by: { kind: "user", user: "Ada" },
}

let container: HTMLDivElement
let root: Root
const client = new QueryClient()

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  container = document.createElement("div")
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<QueryClientProvider client={client}><ConversationInspector thread={thread} info={info} machine="local" open /></QueryClientProvider>))
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe("conversation inspector", () => {
  it("shows real session details without a local collapse control", () => {
    expect(container.textContent).toContain("Ada · user")
    expect(container.textContent).toContain("Project")
    expect(container.textContent).not.toContain("Viewers")
    expect(container.textContent).not.toContain("supports")

    const panel = container.querySelector<HTMLDivElement>("#conversation-inspector-content")!
    expect(panel.hidden).toBe(false)
    expect(container.querySelector('[aria-controls="conversation-inspector-content"]')).toBeNull()
  })

  it("shows only known ACP capabilities", () => {
    const withCapabilities = {
      ...info,
      agent_capabilities: {
        promptCapabilities: { image: false },
        sessionCapabilities: { resume: {}, fork: {} },
      },
    }
    act(() => root.render(<QueryClientProvider client={client}><ConversationInspector thread={thread} info={withCapabilities} machine="local" open /></QueryClientProvider>))
    expect(container.textContent).toContain("ImagesNo")
    expect(container.textContent).toContain("ResumeYes")
    expect(container.textContent).toContain("ForkYes")
    act(() => root.render(<QueryClientProvider client={client}><ConversationInspector thread={thread} info={info} machine="local" open /></QueryClientProvider>))
    expect(container.textContent).not.toContain("This agent supports")
  })
  it("shows changed files from the thread", () => {
    const withDiff: ThreadState = {
      ...thread,
      items: [
        {
          kind: "tool",
          data: {
            title: "Edit",
            content: [{ type: "diff", path: "src/main.go", oldText: "old line\n", newText: "new line\n" }],
          },
        },
      ],
    }
    act(() => root.render(<QueryClientProvider client={client}><ConversationInspector thread={withDiff} info={info} machine="local" open /></QueryClientProvider>))
    const row = container.querySelector<HTMLButtonElement>('button[title="src/main.go"]')!
    expect(row.textContent).toContain("src/main.go")
    expect(container.textContent).toContain("+1")
    expect(container.textContent).toContain("−1")
  })

  it("hides when the toolbar closes the inspector", () => {
    act(() => root.render(<QueryClientProvider client={client}><ConversationInspector thread={thread} info={info} machine="local" open={false} /></QueryClientProvider>))
    expect(container.querySelector<HTMLDivElement>("#conversation-inspector-content")!.parentElement?.hidden).toBe(true)
  })

  it("switches agentpack tools for a chat only", () => {
    expect(container.querySelector("[role=switch]")).toBeNull()
    act(() => root.render(<QueryClientProvider client={client}><ConversationInspector thread={thread} info={{ ...info, transport: "acp", tools: true, started_by: { kind: "browser", user: "a@b.c", agent: "claude" } }} machine="local" open /></QueryClientProvider>))
    expect(container.querySelector("[role=switch]")?.getAttribute("aria-checked")).toBe("true")
    expect(container.textContent).toContain("It can never approve permissions.")
    expect(container.textContent).toContain("Started byClaude")
  })
})
