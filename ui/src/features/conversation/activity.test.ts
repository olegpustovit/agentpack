import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { describe, expect, test } from "vitest"
import type { SessionInfo } from "@/lib/api-types"
import type { ThreadItem, ToolPayload } from "./model"
import { activityCounts, explores, ThreadView, thoughtGist, thoughtSteps, toolOutput, unwrapShell } from "./thread-view"
import { failureReason, machineNameMap, mcpCall, mcpLabel, McpTitle, rawOutputText, sessionNameMap } from "./tool-title"

function render(node: ReturnType<typeof createElement>, fn: (container: HTMLDivElement) => void) {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
  const container = document.createElement("div")
  document.body.appendChild(container)
  const root = createRoot(container)
  try {
    act(() => root.render(node))
    fn(container)
  } finally {
    act(() => root.unmount())
    container.remove()
  }
}

describe("activity rows", () => {
  test("Codex headlines become steps and the latest is the gist", () => {
    const text = "\n\n**Checking logs and metadata**\n\n**Checking diagnostic fields**"
    expect(thoughtSteps(text)).toEqual(["Checking logs and metadata", "Checking diagnostic fields"])
    expect(thoughtGist(text)).toBe("Checking diagnostic fields")
  })

  test("thoughtSteps rejects lines with inner bold spans as prose", () => {
    expect(thoughtSteps("**Check** logs and **fix** bug")).toEqual([])
    expect(thoughtGist("**Check** logs and **fix** bug")).toBe("Check logs and fix bug")
  })

  test("prose thoughts give their first sentence and skip list markers", () => {
    expect(thoughtSteps("The wolf design wins. Next round.")).toEqual([])
    expect(thoughtGist("The **wolf** design wins this round. Next I'll draw.")).toBe("The wolf design wins this round.")
    expect(thoughtGist("1. Check logs\n2. Run tests")).toBe("Check logs")
    expect(thoughtGist("- Inspect state.db\n- Fix parser")).toBe("Inspect state.db")
  })

  test("counts each tool once, MCP first, edits without diff as tool", () => {
    const tool = (kind: string, extra = {}): ThreadItem => ({ kind: "tool", data: { kind, toolCallId: kind + Math.random(), ...extra } }) as ThreadItem
    expect(activityCounts([tool("execute"), tool("execute"), tool("read"), tool("search"), tool("fetch")])).toEqual([
      "2 commands",
      "1 read",
      "1 search",
      "1 tool",
    ])
    // Codex MCP call has kind: "execute" — counted once as MCP, never also as command.
    expect(activityCounts([
      tool("execute", { title: "mcp.agentpack.list_sessions", rawInput: { server: "agentpack", tool: "list_sessions", arguments: {} } }),
      tool("edit", { content: [{ type: "diff", path: "a.go", oldText: "x", newText: "y" }] }),
      tool("edit"),
    ])).toEqual(["1 file changed", "1 agentpack call", "1 tool"])
  })

  test("shell wrappers unwrap only when the whole string is one shell -c command", () => {
    expect(unwrapShell(`/home/u/.nix-profile/bin/bash -lc "sed -n '1,200p' a.go"`)).toBe("sed -n '1,200p' a.go")
    expect(unwrapShell("git status")).toBe("git status")
    expect(unwrapShell('bash -lc "git status" && rm -rf "dist"')).toBe('bash -lc "git status" && rm -rf "dist"')
  })

  test("explores accepts read-only commands and rejects mutating or compound traps", () => {
    const sh = (command: string) => ({ kind: "execute", status: "completed", rawInput: { command } }) as ToolPayload
    // True cases
    expect(explores(sh("git log -20 --oneline; tail -140 AGENTS.md"))).toBe(true)
    expect(explores(sh("/bin/bash -lc \"sed -n '1,20p' a.go\""))).toBe(true)
    expect(explores(sh("rg -n 'func a\\(|func b' internal 2>/dev/null; sed -n '1,9p' x.go"))).toBe(true)
    expect(explores(sh("find . -name '*.go'"))).toBe(true)
    expect(explores(sh("git branch"))).toBe(true)
    expect(explores(sh("git branch -a"))).toBe(true)
    expect(explores(sh("git branch -r"))).toBe(true)
    expect(explores(sh("git branch -v"))).toBe(true)
    expect(explores(sh("git branch --list"))).toBe(true)
    // Mutating / unsafe false cases
    expect(explores(sh("git status\nrm -rf dist"))).toBe(false)
    expect(explores(sh("ls & rm -rf dist"))).toBe(false)
    expect(explores(sh('ls "$(rm -rf /)"'))).toBe(false)
    expect(explores(sh("ls `rm -rf /`"))).toBe(false)
    expect(explores(sh("find . -delete"))).toBe(false)
    expect(explores(sh("find . -exec rm {} +"))).toBe(false)
    expect(explores(sh("find . -ok rm {} ;"))).toBe(false)
    expect(explores(sh("find . -fprintf out '%p'"))).toBe(false)
    expect(explores(sh("git branch -D main"))).toBe(false)
    expect(explores(sh("git branch new-branch"))).toBe(false)
    expect(explores(sh("sed -n -i '1p' a.go"))).toBe(false)
    expect(explores(sh("sed -ni '1p' a.go"))).toBe(false)
    expect(explores(sh("sed -n '1w /tmp/out' a.go"))).toBe(false)
    expect(explores(sh('bash -lc "git status" && rm -rf "dist"'))).toBe(false)
    expect(explores(sh("sed -i 's/a/b/' a.go"))).toBe(false)
    expect(explores(sh("cat > a.py <<'EOF'"))).toBe(false)
    expect(explores(sh("go test ./..."))).toBe(false)
    // MCP calls with kind: "execute" never explore.
    expect(explores({ kind: "execute", status: "completed", title: "mcp.agentpack.list_sessions", rawInput: { server: "agentpack", tool: "list_sessions", arguments: {} } } as ToolPayload)).toBe(false)
  })
})

describe("MCP calls", () => {
  const names = (id: string) => (id === "session-c501" ? "Images not rendered" : id)

  test("parses Claude, Codex, OMP and Antigravity shapes and rejects non-MCP titles", () => {
    // Claude current (_meta.jetbrains.air.mcp preferred)
    expect(mcpCall({
      title: "mcp__ignored__tool",
      rawInput: { session_id: "s1" },
      _meta: { jetbrains: { air: { mcp: { server: "agentpack", tool: "read_conversation" } } }, claudeCode: { toolName: "mcp__other__x" } },
    } as unknown as ToolPayload)).toEqual({ server: "agentpack", tool: "read_conversation", args: { session_id: "s1" } })
    // Claude legacy
    expect(mcpCall({ title: "mcp__agentpack__read_conversation" } as ToolPayload)).toEqual({ server: "agentpack", tool: "read_conversation" })
    expect(mcpCall({ title: "mcp__plugin_x_playwright__browser_click" } as ToolPayload)).toEqual({ server: "plugin_x_playwright", tool: "browser_click" })
    // Codex (server/tool from rawInput, args = rawInput.arguments; dot-containing tool)
    expect(mcpCall({
      kind: "execute",
      title: "mcp.context7.query.docs",
      rawInput: { server: "context7", tool: "query.docs", arguments: { topic: "hooks" } },
    } as ToolPayload)).toEqual({ server: "context7", tool: "query.docs", args: { topic: "hooks" } })
    expect(mcpCall({ kind: "execute", title: "mcp.my_srv.foo.bar" } as ToolPayload)).toEqual({ server: "my_srv", tool: "foo.bar" })
    // OMP xd:// wrapper + malformed JSON guard + direct form
    expect(mcpCall({
      kind: "execute",
      title: "Inspect active sessions",
      rawInput: { path: "xd://mcp__agentpack_list_sessions", content: '{"node":"n1"}' },
    } as ToolPayload)).toEqual({ server: "agentpack", tool: "list_sessions", args: { node: "n1" }, fallback: "Inspect active sessions" })
    expect(mcpCall({
      kind: "execute",
      title: "Broken JSON",
      rawInput: { path: "xd://mcp__agentpack_list_nodes", content: "{bad" },
    } as ToolPayload)).toEqual({ server: "agentpack", tool: "list_nodes", fallback: "Broken JSON" })
    expect(mcpCall({ title: "mcp__agentpack_list_sessions" } as ToolPayload)).toEqual({ server: "agentpack", tool: "list_sessions" })
    // Antigravity (args minus toolSummary, fallback = toolSummary)
    expect(mcpCall({
      title: "Run mcp_agentpack_list_sessions",
      rawInput: { node: "n1", toolSummary: "List chats" },
    } as ToolPayload)).toEqual({ server: "agentpack", tool: "list_sessions", args: { node: "n1" }, fallback: "List chats" })
    // Invalid / non-MCP names rejected
    expect(mcpCall({ title: "Run mcp_agentpack_check.sh --dry-run" } as ToolPayload)).toBeUndefined()
    expect(mcpCall({ title: "mcp__agentpack__bad name" } as ToolPayload)).toBeUndefined()
    expect(mcpCall({ title: "Read file" } as ToolPayload)).toBeUndefined()
  })

  test("agentpack calls format real schema targets and fallbacks", () => {
    expect(mcpLabel({ server: "agentpack", tool: "read_conversation" }, { session_id: "session-c501", node: "n1" }, names, () => "Mac mini")).toEqual(["Read chat", "Images not rendered on Mac mini"])
    expect(mcpLabel({ server: "agentpack", tool: "wait" }, { session_ids: ["session-c501"] }, names, String)).toEqual(["Waited for", "Images not rendered"])
    expect(mcpLabel({ server: "agentpack", tool: "wait" }, { task_ids: ["t_1", "t_2"] }, names, String)).toEqual(["Waited for", "2 tasks"])
    expect(mcpLabel({ server: "agentpack", tool: "wait" }, { session_ids: ["session-c501"], task_ids: ["t_1"] }, names, String)).toEqual(["Waited for", "Images not rendered, 1 task"])
    expect(mcpLabel({ server: "agentpack", tool: "install_package" }, { spec: "github:org/skill", directory: "/work/agentpack" }, names, String)).toEqual(["Installed", "github:org/skill in agentpack"])
    expect(mcpLabel({ server: "agentpack", tool: "get_issue" }, { fingerprint: "fp_1234" }, names, String)).toEqual(["Read issue", "fp_1234"])
    expect(mcpLabel({ server: "agentpack", tool: "resolve_issue" }, { fingerprint: "fp_1234", version: "0.14.5" }, names, String)).toEqual(["Resolved issue", "fp_1234"])
    expect(mcpLabel({ server: "agentpack", tool: "get_trace" }, { trace_id: "abcdef012345" }, names, String)).toEqual(["Read a trace", "abcdef012345"])
    expect(mcpLabel({ server: "agentpack", tool: "list_nodes" }, {}, names, String)).toEqual(["Listed machines", ""])
    expect(mcpLabel({ server: "agentpack", tool: "list_nodes", fallback: "Check tailnet" }, {}, names, String)).toEqual(["Listed machines", "Check tailnet"])
    expect(mcpLabel({ server: "playwright", tool: "browser_click" }, {}, names, String)).toEqual(["browser click", "playwright"])
  })

  test("extracts failure reasons from fenced text, string rawOutput, and Codex rawOutput objects", () => {
    expect(failureReason("```\nconversation unavailable\n```")).toBe("conversation unavailable")
    expect(failureReason("", "plain error\nmore")).toBe("plain error")
    expect(failureReason("", { error: "socket closed" })).toBe("socket closed")
    expect(failureReason("", { error: { message: "session not found" } })).toBe("session not found")
    expect(failureReason("", { result: { content: [{ type: "text", text: "conversation unavailable" }] } })).toBe("conversation unavailable")
    expect(rawOutputText({ result: { content: [{ text: "line 1" }, { text: "line 2" }] } })).toBe("line 1\nline 2")
    expect(toolOutput({ rawOutput: { error: { message: "denied" } } } as ToolPayload)).toBe("denied")
  })

  test("sessionNameMap uses title fallback and machineNameMap resolves local node too", () => {
    const list = [
      { id: "session-a1", name: "Named chat", title: "Ignored title" },
      { id: "session-b2", name: "", title: "First prompt title" },
    ] as SessionInfo[]
    const map = sessionNameMap(list)
    expect(map.get("session-a1")).toBe("Named chat")
    expect(map.get("b2")).toBe("First prompt title")

    const machines = machineNameMap(
      [{ id: "local", name: "Mac mini" }, { id: "node-peer", name: "oracle-vm" }],
      { id: "node-local-id", name: "Mac mini" },
    )
    expect(machines.get("local")).toBe("Mac mini")
    expect(machines.get("node-local-id")).toBe("Mac mini")
    expect(machines.get("node-peer")).toBe("oracle-vm")
  })
})

describe("activity rendering", () => {
  test("a single thought stays an ActivityGroup with live shimmer when working", () => {
    const thought: ThreadItem = { kind: "agent_thought_chunk", seq: 1, ts: 1000, data: { text: { text: "Checking state.db." } } }
    render(createElement(ThreadView, { items: [thought], machine: "local", sessionId: "s", working: true }), (container) => {
      const group = container.querySelector('[data-slot="tool-group"]')!
      expect(group).not.toBeNull()
      const title = group.querySelector("summary span")!
      expect(title.textContent).toBe("Checking state.db.")
      expect(title.classList.contains("shimmer")).toBe(true)
      expect(title.classList.contains("text-(--fg)")).toBe(false)
    })
  })

  test("end time ignores a later user message and uses a following agent reply", () => {
    const baseItems: ThreadItem[] = [
      { kind: "agent_thought_chunk", seq: 1, ts: 0, data: { text: { text: "**Working**" } } },
      { kind: "tool", seq: 2, ts: 0, endTs: 5_000, data: { title: "Read a.go", kind: "read", status: "completed" } },
    ]
    render(createElement(ThreadView, {
      items: [...baseItems, { kind: "user_message_chunk", seq: 3, ts: 3_600_000, data: { text: { text: "An hour later" } } }],
      machine: "local",
      sessionId: "s",
    }), (container) => {
      const text = container.querySelector('[data-slot="tool-group"] summary')!.textContent!
      expect(text).toContain("5s")
      expect(text).not.toContain("1h")
    })
    render(createElement(ThreadView, {
      items: [...baseItems, { kind: "agent_message_chunk", seq: 3, ts: 12_000, data: { text: { text: "Done" } } }],
      machine: "local",
      sessionId: "s",
    }), (container) => {
      expect(container.querySelector('[data-slot="tool-group"] summary')!.textContent).toContain("12s")
    })
  })

  test("ThoughtBlock prose uses an accessible expand/collapse button with dimmed markdown", () => {
    const items: ThreadItem[] = [
      { kind: "agent_thought_chunk", seq: 1, ts: 0, data: { text: { text: "First sentence of prose. Second sentence." } } },
    ]
    render(createElement(ThreadView, { items, machine: "local", sessionId: "s" }), (container) => {
      const group = container.querySelector<HTMLDetailsElement>('[data-slot="tool-group"]')!
      act(() => { group.open = true; group.dispatchEvent(new Event("toggle")) })
      const button = container.querySelector<HTMLButtonElement>('[data-slot="thought-block"] button')!
      expect(button.type).toBe("button")
      expect(button.getAttribute("aria-expanded")).toBe("false")
      expect(button.className).toContain("[&_.md]:text-(--fg3)")
      act(() => button.click())
      expect(button.getAttribute("aria-expanded")).toBe("true")
      act(() => button.click())
      expect(button.getAttribute("aria-expanded")).toBe("false")
    })
  })

  test("non-agentpack McpTitle renders without QueryClient and Codex MCP renders as MCP row, not terminal", () => {
    // No QueryClientProvider proves non-agentpack calls subscribe to zero queries.
    render(createElement(McpTitle, { call: { server: "playwright", tool: "browser_click" } }), (container) => {
      expect(container.textContent).toBe("browser click playwright")
    })
    const codexFailed: ThreadItem = {
      kind: "tool",
      seq: 1,
      data: {
        kind: "execute",
        status: "failed",
        title: "mcp.agentpack.read_conversation",
        rawInput: { server: "agentpack", tool: "read_conversation", arguments: { session_id: "session-c501" } },
        rawOutput: { error: { message: "conversation unavailable" } },
      },
    }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(createElement(QueryClientProvider, { client }, createElement(ThreadView, { items: [codexFailed], machine: "local", sessionId: "s" })), (container) => {
      const row = container.querySelector('[data-slot="tool-row"]')!
      expect(row).not.toBeNull()
      expect(container.querySelector('[data-slot="tool-terminal-block"]')).toBeNull()
      expect(row.textContent).toContain("Read chat")
      expect(row.textContent).toContain("conversation unavailable")
    })
  })
})
