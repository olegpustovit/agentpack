import { describe, expect, test } from "vitest"
import type { ThreadItem, ToolPayload } from "./model"
import { activityCounts, explores, thoughtGist, thoughtSteps, unwrapShell } from "./thread-view"
import { failureReason, mcpCall, mcpLabel } from "./tool-title"

describe("activity rows", () => {
  test("Codex headlines become steps and the latest is the gist", () => {
    const text = "\n\n**Checking logs and metadata**\n\n**Checking diagnostic fields**"
    expect(thoughtSteps(text)).toEqual(["Checking logs and metadata", "Checking diagnostic fields"])
    expect(thoughtGist(text)).toBe("Checking diagnostic fields")
  })
  test("prose thoughts give their first sentence", () => {
    expect(thoughtSteps("The wolf design wins. Next round.")).toEqual([])
    expect(thoughtGist("The **wolf** design wins this round. Next I'll draw.")).toBe("The wolf design wins this round.")
  })
  test("counts by verb", () => {
    const tool = (kind: string): ThreadItem => ({ kind: "tool", data: { kind, toolCallId: kind + Math.random() } }) as ThreadItem
    expect(activityCounts([tool("execute"), tool("execute"), tool("read"), tool("search"), tool("fetch")])).toEqual(["2 commands", "1 read", "1 search", "1 tool"])
  })
  test("shell wrappers unwrap", () => {
    expect(unwrapShell(`/home/u/.nix-profile/bin/bash -lc "sed -n '1,200p' a.go"`)).toBe("sed -n '1,200p' a.go")
    expect(unwrapShell("git status")).toBe("git status")
  })
})

describe("MCP calls", () => {
  const names = (id: string) => (id === "session-c501" ? "Images not rendered" : id)
  test("parses Claude and Antigravity names", () => {
    expect(mcpCall({ title: "mcp__agentpack__read_conversation" } as ToolPayload)).toEqual({ server: "agentpack", tool: "read_conversation" })
    expect(mcpCall({ title: "Run mcp_agentpack_list_sessions" } as ToolPayload)).toEqual({ server: "agentpack", tool: "list_sessions" })
    expect(mcpCall({ title: "mcp__plugin_x_playwright__browser_click" } as ToolPayload)).toEqual({ server: "plugin_x_playwright", tool: "browser_click" })
    expect(mcpCall({ title: "Read file" } as ToolPayload)).toBeUndefined()
  })
  test("agentpack calls read as a verb and a named target", () => {
    expect(mcpLabel({ server: "agentpack", tool: "read_conversation" }, { session_id: "session-c501", node: "n1" }, names, () => "Mac mini")).toEqual(["Read chat", "Images not rendered on Mac mini"])
    expect(mcpLabel({ server: "agentpack", tool: "wait" }, { session_ids: ["session-c501"] }, names, String)).toEqual(["Waited for", "Images not rendered"])
    expect(mcpLabel({ server: "agentpack", tool: "list_nodes" }, {}, names, String)).toEqual(["Listed machines", ""])
    expect(mcpLabel({ server: "playwright", tool: "browser_click" }, {}, names, String)).toEqual(["browser click", "playwright"])
    expect(failureReason("```\nconversation unavailable\n```")).toBe("conversation unavailable")
  })
  test("only looking shell commands explore", () => {
    const sh = (command: string) => ({ kind: "execute", status: "completed", rawInput: { command } }) as ToolPayload
    expect(explores(sh("git log -20 --oneline; tail -140 AGENTS.md"))).toBe(true)
    expect(explores(sh("/bin/bash -lc \"sed -n '1,20p' a.go\""))).toBe(true)
    expect(explores(sh("sed -i 's/a/b/' a.go"))).toBe(false)
    expect(explores(sh("cat > a.py <<'EOF'"))).toBe(false)
    expect(explores(sh("go test ./..."))).toBe(false)
    expect(explores(sh("rg -n 'func a\\(|func b' internal 2>/dev/null; sed -n '1,9p' x.go"))).toBe(true)
  })
})
