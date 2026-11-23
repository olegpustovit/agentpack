import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { beforeAll, describe, expect, it } from "vitest"
import { ToolDiffBody } from "./tool-diff"
import { markdownReady } from "./agent-markdown"

beforeAll(() => markdownReady)

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
