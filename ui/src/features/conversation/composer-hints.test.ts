import { describe, expect, it } from "vitest"
import { afterEach } from "vitest"
import { agentCommands, applyHint, cachedCommands, commandSpans, filterAtHints, filterSlashHints, moveHint, rememberCommands } from "./composer-hints"

describe("composer hints", () => {
  afterEach(() => localStorage.clear())
  it("lists the agent's own commands with the gateway's", () => {
    const commands = agentCommands([
      { kind: "available_commands_update", payload: { availableCommands: [{ name: "old" }] } },
      { kind: "available_commands_update", payload: { availableCommands: [{ name: "compact", description: "Compact" }, { name: "agentpack-bundle:ponytail" }] } },
    ])
    expect(filterSlashHints("/", commands).map((item) => item.value)).toEqual(["/goal", "/compact", "/fork", "/terminal", "/agentpack-bundle:ponytail"])
    expect(filterSlashHints("/pony", commands).map((item) => item.value)).toEqual(["/agentpack-bundle:ponytail"])
    expect(filterSlashHints("/compact now", commands)).toEqual([])
  })

  it("matches the agent's commands mid-sentence and the gateway's only at a line start", () => {
    const commands = [{ name: "review", input: { hint: "path" } }]
    expect(filterSlashHints("context first\n/rev", commands)[0]).toMatchObject({ value: "/review", hint: "path", source: "agent" })
    expect(filterSlashHints("please /", commands).map((item) => item.value)).toEqual(["/review"])
    expect(filterSlashHints("a/rev", commands)).toEqual([])
    expect(applyHint("context first\n/rev", { value: "/review", description: "" })).toBe("context first\n/review ")
    expect(applyHint("please /re", { value: "/review", description: "" })).toBe("please /review ")
  })

  it("marks the commands a draft would run", () => {
    const spans = commandSpans("/goal ship\nrun /review and /goal x /usr/bin", [{ name: "review" }])
    expect(spans.filter((span) => span.command).map((span) => span.text)).toEqual(["/goal", "/review"])
    expect(spans.map((span) => span.text).join("")).toBe("/goal ship\nrun /review and /goal x /usr/bin")
  })

  it("lists gateway commands before the first update and caches each agent's list", () => {
    expect(agentCommands([])).toBeUndefined()
    expect(filterSlashHints("/go").map((item) => item.value)).toEqual(["/goal"])
    rememberCommands("claude", [{ name: "review" }])
    expect(cachedCommands("claude")).toEqual([{ name: "review" }])
    expect(cachedCommands("codex")).toEqual([])
  })

  it("filters @ file hints", () => {
    const hits = filterAtHints("see @term", ["internal/network/terminal_client.go"])
    expect(hits[0]?.value).toBe("@internal/network/terminal_client.go")
  })

  it("replaces the @ fragment when a hint is chosen", () => {
    expect(applyHint("look @ter", { value: "@internal/a.go", description: "" })).toBe("look @internal/a.go ")
  })

  it("triggers @ only at the start or after whitespace", () => {
    const paths = ["a/term.go"]
    expect(filterAtHints("me@term", paths)).toEqual([])
    expect(filterAtHints("@term", paths)).toHaveLength(1)
    expect(filterAtHints("see\n@term", paths)).toHaveLength(1)
  })

  it("wraps the active hint index", () => {
    expect(moveHint(0, -1, 3)).toBe(2)
    expect(moveHint(2, 1, 3)).toBe(0)
    expect(moveHint(0, 1, 0)).toBe(0)
  })
})
