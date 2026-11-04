import { act } from "react"
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

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  container = document.createElement("div")
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<ConversationInspector thread={thread} info={info} machine="local" open />))
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
    act(() => root.render(<ConversationInspector thread={thread} info={withCapabilities} machine="local" open />))
    expect(container.textContent).toContain("ImagesNo")
    expect(container.textContent).toContain("ResumeYes")
    expect(container.textContent).toContain("ForkYes")
    act(() => root.render(<ConversationInspector thread={thread} info={info} machine="local" open />))
    expect(container.textContent).not.toContain("Agent capabilities")
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
    act(() => root.render(<ConversationInspector thread={withDiff} info={info} machine="local" open />))
    expect(container.textContent).toContain("main.go")
    expect(container.textContent).toContain("+1")
    expect(container.textContent).toContain("−1")
  })

  it("hides when the toolbar closes the inspector", () => {
    act(() => root.render(<ConversationInspector thread={thread} info={info} machine="local" open={false} />))
    expect(container.querySelector<HTMLDivElement>("#conversation-inspector-content")!.parentElement?.hidden).toBe(true)
  })
})
