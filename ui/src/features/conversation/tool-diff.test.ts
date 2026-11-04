import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { describe, expect, it } from "vitest"
import { ToolDiffBody, toolDiffUsesWordMarks } from "./tool-diff"

describe("toolDiffUsesWordMarks", () => {
  it("marks changed tokens on edited lines", () => {
    const oldText = "\tconn, err := dial(ctx, record, 0)\n"
    const newText = "\tsince := record.lastOffset.Load()\n\tconn, err := dial(ctx, record, since)\n"
    expect(toolDiffUsesWordMarks(oldText, newText)).toBe(true)
  })
})
describe("ToolDiffBody", () => {
  it("syntax-highlights Go diff lines while retaining word marks", () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const container = document.createElement("div")
    const root = createRoot(container)
    act(() => root.render(createElement(ToolDiffBody, {
      content: [{ type: "diff", path: "internal/network/worker.go", oldText: "return 0\n", newText: "return 1\n" }],
    })))
    expect(container.querySelector(".hljs-keyword")?.textContent).toBe("return")
    expect([...container.querySelectorAll("mark")].map((mark) => mark.textContent)).toEqual(["0", "1"])
    act(() => root.unmount())
  })
})
