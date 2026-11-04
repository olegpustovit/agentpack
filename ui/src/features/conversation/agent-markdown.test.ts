import { describe, expect, it } from "vitest"
import { renderAgentMarkdown } from "./agent-markdown"

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
