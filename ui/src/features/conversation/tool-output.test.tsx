import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { describe, expect, it } from "vitest"
import { classifyOutput, languageForPath, PREVIEW_LINES, ToolOutput } from "./tool-output"

describe("classifyOutput", () => {
  it("moves Read and cat -n line numbers into a gutter and takes the language from the path", () => {
    const read = classifyOutput("     1→package main\n     2→\n     3→func main() {}", { path: "/w/cmd/main.go" })
    expect(read.language).toBe("go")
    expect(read.lines).toEqual([{ n: 1, text: "package main" }, { n: 2, text: "" }, { n: 3, text: "func main() {}" }])
    expect(classifyOutput("  10\tx = 1\n  11\ty = 2").lines.map((line) => line.n)).toEqual([10, 11])
  })

  it("recognises diffs and JSON without a path", () => {
    expect(classifyOutput("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b").language).toBe("diff")
    expect(classifyOutput('{"ok": true, "items": [1, 2]}').language).toBe("json")
    expect(classifyOutput("{ not json").language).toBeUndefined()
  })

  it("tones shell output lines, including the harness's bracketed notes", () => {
    const tones = classifyOutput("ok  \tgithub.com/x/y\t0.3s\n--- FAIL: TestX\npanic: boom\nwarning: deprecated flag\nplain\n[This command modified 1 file you've previously read.]")
      .lines.map((line) => line.tone)
    expect(tones).toEqual(["ok", "error", "error", "warn", undefined, "note"])
  })

  it("maps common extensions", () => {
    expect(["a.tsx", "b.py", "Cargo.toml", "x.yml", "README"].map(languageForPath)).toEqual(["typescript", "python", "toml", "yaml", undefined])
  })
})

describe("ToolOutput", () => {
  it("highlights code and previews long output", () => {
    const long = Array.from({ length: PREVIEW_LINES + 5 }, (_, i) => `${String(i + 1).padStart(6)}→const v${i} = ${i}`).join("\n")
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const container = document.createElement("div")
    const root = createRoot(container)
    act(() => root.render(createElement(ToolOutput, { text: long, path: "x.ts" })))
    expect(container.querySelector(".hljs-keyword")?.textContent).toBe("const")
    expect(container.querySelectorAll(".hljs-keyword")).toHaveLength(PREVIEW_LINES)
    expect(container.querySelector("button")?.textContent).toBe(`Show all ${(PREVIEW_LINES + 5).toLocaleString()} lines`)
    act(() => container.querySelector("button")!.click())
    expect(container.querySelectorAll(".hljs-keyword")).toHaveLength(PREVIEW_LINES + 5)
    act(() => root.unmount())
  })
})
