import { describe, expect, it } from "vitest"
import { configRole, dangerousMode, parseConfigOptions } from "./config-options"

describe("parseConfigOptions", () => {
  it("reads select and boolean values from ACP configOptions JSON", () => {
    expect(parseConfigOptions([
      {
        type: "select",
        id: "mode",
        name: "Permissions",
        currentValue: "default",
        options: [{ value: "default", name: "Default" }, { value: "plan", name: "Plan" }],
      },
      { type: "boolean", id: "verbose", name: "Verbose output", currentValue: false },
    ])).toEqual([
      {
        type: "select",
        id: "mode",
        name: "Permissions",
        value: "default",
        items: [
          { value: "default", label: "Default" },
          { value: "plan", label: "Plan" },
        ],
      },
      { type: "boolean", id: "verbose", name: "Verbose output", value: false },
    ])
  })

  it("uses the current ACP mode when configOptions is absent", () => {
    expect(parseConfigOptions({
      modes: {
        currentModeId: "plan",
        availableModes: [
          { id: "default", name: "Default" },
          { id: "plan", name: "Plan" },
        ],
      },
    })).toEqual([{
      type: "select",
      id: "mode",
      name: "Permissions",
      value: "plan",
      items: [{ value: "default", label: "Default" }, { value: "plan", label: "Plan" }],
    }])
  })

  it("prefers configOptions and leaves unknown config state empty", () => {
    expect(parseConfigOptions({
      configOptions: [{ type: "select", id: "model", name: "Model", currentValue: "opus", options: [{ value: "opus", name: "Opus" }] }],
      modes: { currentModeId: "plan", availableModes: [{ id: "plan", name: "Plan" }] },
    })).toMatchObject([{ type: "select", id: "model", value: "opus" }])
    expect(parseConfigOptions({ configOptions: [], modes: { currentModeId: "plan", availableModes: [{ id: "plan", name: "Plan" }] } })).toEqual([])
    expect(parseConfigOptions({})).toEqual([])
  })

  it("groups ACP mode, model, and thought-level options", () => {
    const options = parseConfigOptions([
      { type: "select", id: "mode", currentValue: "plan", options: [{ value: "plan" }] },
      { type: "select", id: "openai/model", category: "model", currentValue: "gpt", options: [{ value: "gpt" }] },
      { type: "select", id: "reasoning", category: "thought_level", currentValue: "high", options: [{ value: "high" }] },
    ])
    expect(options.map(configRole)).toEqual(["permissions", "model", "effort"])
  })

  it("reads model choices grouped by the agent", () => {
    expect(parseConfigOptions([{
      id: "model", type: "select", currentValue: "sol",
      options: [{ group: "openai", name: "OpenAI", options: [{ value: "sol", name: "Sol" }] }],
    }])).toMatchObject([{ items: [{ value: "sol", label: "Sol", group: "OpenAI" }] }])
  })
})

describe("dangerousMode", () => {
  it("flags modes that skip approvals", () => {
    expect(dangerousMode({ value: "bypassPermissions", label: "Bypass Permissions" })).toBe(true)
    expect(dangerousMode({ value: "full-access", label: "Full Access" })).toBe(true)
    expect(dangerousMode({ value: "acceptEdits", label: "Accept edits" })).toBe(false)
    expect(dangerousMode({ value: "auto", label: "Auto" })).toBe(false)
  })
})
