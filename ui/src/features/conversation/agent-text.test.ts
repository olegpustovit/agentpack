import { describe, expect, it } from "vitest"
import { stripAgentText } from "./agent-text"

describe("stripAgentText", () => {
  it("drops OSC injection and ANSI from agent chunks", () => {
    expect(stripAgentText("\u001b]52;c;evil\u0007injected")).toBe("injected")
  })
})
