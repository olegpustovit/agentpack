import { beforeAll, describe, expect, it } from "vitest"
import { markdownReady, renderAgentBlocks } from "./agent-markdown"

const renderAgentMarkdown = (text: string) => renderAgentBlocks(text).join("")


beforeAll(() => markdownReady)
describe("renderAgentMarkdown", () => {
  it("highlights known fenced languages", () => {
    const html = renderAgentMarkdown("```go\nfunc main() {}\n```")
    expect(html).toContain('class="hljs language-go"')
    expect(html).toContain("func")
  })

  it("escapes raw HTML and permits only HTTP links and no images", () => {
    const html = renderAgentMarkdown('<span>raw</span>\n\n![image](https://evil.test/a.png)\n\n[mail](mailto:hi@example.test) [web](https://example.test)')
    expect(html).toContain("&lt;span&gt;raw&lt;/span&gt;")
    expect(html).not.toContain("<span>")
    expect(html).not.toContain("<img")
    expect(html).not.toContain("mailto:")
    expect(html).toContain('href="https://example.test"')
  })
})

describe("renderAgentBlocks", () => {
  it("never shows half-typed markers while streaming", () => {
    expect(renderAgentBlocks("Some **bold", true).join("")).toContain("<strong>bold</strong>")
    const fence = renderAgentBlocks("Run:\n\n```go\nfunc main() {", true).join("")
    expect(fence).toContain("language-go")
    expect(fence).not.toContain("```")
    const table = renderAgentBlocks("| a | b |\n| - | - |\n| 1 | 2 |", true).join("")
    expect(table).toContain("<table>")
  })

  it("keeps finished blocks byte-identical as the reply grows", () => {
    const first = renderAgentBlocks("# Title\n\nFirst paragraph.\n\nSecond par", true)
    const next = renderAgentBlocks("# Title\n\nFirst paragraph.\n\nSecond paragraph grows", true)
    expect(next.slice(0, 2)).toEqual(first.slice(0, 2))
    expect(next).toHaveLength(3)
  })

  it("gives fenced code a header with a copy button and resolves reference links", () => {
    const blocks = renderAgentBlocks("```sh\nls\n```\n\nsee [docs][d]\n\n[d]: https://example.test")
    expect(blocks[0]).toContain("data-copy-code")
    expect(blocks.join("")).toContain('href="https://example.test"')
  })
})

describe("streamed blocks", () => {
  it("keeps finished blocks identical while the tail grows, and leaves unlabelled code plain", () => {
    const first = renderAgentBlocks("Intro paragraph.\n\n```\nlet x = 1\n```\n\nTail", true)
    const next = renderAgentBlocks("Intro paragraph.\n\n```\nlet x = 1\n```\n\nTail grows", true)
    expect(next[0]).toBe(first[0])
    expect(next[1]).toBe(first[1])
    expect(first[1]).not.toContain("hljs-")
    expect(next[2]).toContain("Tail grows")
  })
})
