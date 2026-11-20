import { describe, expect, it } from "vitest"
import { diffAnchorId, fileLabel, groupByPath, listDiffChanges, reviewPrompt } from "./diff-summary"
import type { ThreadItem } from "./model"

describe("listDiffChanges", () => {
  it("counts lines from tool diff blocks", () => {
    const items: ThreadItem[] = [
      {
        kind: "tool",
        data: {
          content: [
            {
              type: "diff",
              path: "internal/network/terminal_client.go",
              oldText: "\tconn, err := dial(ctx, record, 0)\n",
              newText: "\tsince := record.lastOffset.Load()\n\tconn, err := dial(ctx, record, since)\n",
            },
          ],
        },
      },
    ]
    expect(listDiffChanges(items)).toEqual([
      expect.objectContaining({ path: "internal/network/terminal_client.go", add: 2, del: 1, toolId: undefined, turn: 0 }),
    ])
  })
})

describe("per-turn grouping", () => {
  const edit = (id: string, path: string, newText: string): ThreadItem => ({
    kind: "tool",
    data: { toolCallId: id, content: [{ type: "diff", path, oldText: "", newText }] },
  })
  const items: ThreadItem[] = [
    { kind: "prompt" },
    edit("t1", "a.go", "x\n"),
    { kind: "prompt" },
    edit("t2", "a.go", "x\ny\n"),
    edit("t3", "b.go", "z\n"),
  ]
  it("tags turns and anchors the latest edit uniquely", () => {
    const all = listDiffChanges(items)
    expect(all.map((c) => c.turn)).toEqual([1, 2, 2])
    const grouped = groupByPath(all)
    expect(grouped.map((c) => [c.path, c.add, c.anchor])).toEqual([
      ["a.go", 3, diffAnchorId("a.go", "t2")],
      ["b.go", 1, diffAnchorId("b.go", "t3")],
    ])
    expect(diffAnchorId("a.go", "t1")).not.toBe(diffAnchorId("a.go", "t2"))
  })
  it("latest turn only", () => {
    const all = listDiffChanges(items)
    expect(groupByPath(all.filter((c) => c.turn === 1)).map((c) => c.path)).toEqual(["a.go"])
  })
})

describe("fileLabel", () => {
  it("names the file and its folder inside the project, or the whole path outside it", () => {
    expect(fileLabel("/home/u/p/internal/network/mcp_bridge.go", "/home/u/p/")).toEqual({ name: "mcp_bridge.go", folder: "internal/network" })
    expect(fileLabel("/home/u/p/go.mod", "/home/u/p")).toEqual({ name: "go.mod", folder: "" })
    expect(fileLabel("/etc/hosts", "/home/u/p")).toEqual({ name: "hosts", folder: "/etc" })
    expect(fileLabel("README.md")).toEqual({ name: "README.md", folder: "" })
  })
})

describe("reviewPrompt", () => {
  it("sends one path:line line per comment, flattened and without blanks", () => {
    expect(
      reviewPrompt([
        { path: "internal/network/terminal_client.go", line: 212, text: "Clamp the offset.\nLast() can be ahead." },
        { path: "a.go", line: 3, text: "  " },
        { path: "replay.go", line: 1, text: " Rename this. " },
      ]),
    ).toBe("internal/network/terminal_client.go:212: Clamp the offset. Last() can be ahead.\nreplay.go:1: Rename this.")
  })
})
