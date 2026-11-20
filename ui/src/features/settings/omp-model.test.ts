import { describe, expect, it } from "vitest"
import {
  quickRoles,
  effectiveValue,
  formatRoleModel,
  isChanged,
  missingMachines,
  parseRoleModel,
  readOmpSettings,
  visibleSettings,
  withOverride,
  type OmpSetting,
} from "./omp-model"

const setting = (key: string, tab: string, label: string, description = ""): OmpSetting => ({ key, tab, group: "", label, description, type: "boolean" })

describe("role models", () => {
  it("parses a selector with an optional known thinking level", () => {
    expect(parseRoleModel("anthropic/claude-opus-5-5:high")).toEqual({ selector: "anthropic/claude-opus-5-5", provider: "anthropic", level: "high" })
    expect(parseRoleModel("local/qwen3:1.7b")).toEqual({ selector: "local/qwen3:1.7b", provider: "local", level: "" })
    expect(parseRoleModel("openrouter/google/gemini:max")).toEqual({ selector: "openrouter/google/gemini", provider: "openrouter", level: "max" })
    expect(parseRoleModel(undefined)).toBeUndefined()
    expect(parseRoleModel("@smol")).toBeUndefined()
  })

  it("formats the selector and level back", () => {
    expect(formatRoleModel("anthropic/claude-opus-5-5", "xhigh")).toBe("anthropic/claude-opus-5-5:xhigh")
    expect(formatRoleModel("web/exa", "")).toBe("web/exa")
  })
})

describe("effective values", () => {
  const settings = readOmpSettings({
    version: "18.20.2",
    schema: [],
    values: { steeringMode: "all", "git.enabled": true, modelRoles: { default: "anthropic/a", smol: "anthropic/b" } },
    network: { "git.enabled": false, "modelRoles.smol": "openai/c:low" },
  })

  it("prefers the network override and reads flat or nested keys", () => {
    expect(effectiveValue(settings, "git.enabled")).toBe(false)
    expect(effectiveValue(settings, "steeringMode")).toBe("all")
    expect(effectiveValue(settings, "modelRoles.default")).toBe("anthropic/a")
    expect(effectiveValue(settings, "modelRoles.smol")).toBe("openai/c:low")
    expect(effectiveValue(settings, "missing")).toBeUndefined()
  })

  it("marks only network keys as changed", () => {
    expect(isChanged(settings, "git.enabled")).toBe(true)
    expect(isChanged(settings, "steeringMode")).toBe(false)
    expect(isChanged(settings, "modelRoles.smol")).toBe(true)
    expect(isChanged(settings, "modelRoles.default")).toBe(false)
  })

  it("writes and removes overrides, including nested copies", () => {
    const nested = { ...settings, network: { modelRoles: { plan: "openai/d" } } }
    expect(isChanged(withOverride(nested, "modelRoles.plan", undefined), "modelRoles.plan")).toBe(false)
    expect(effectiveValue(withOverride(settings, "steeringMode", "one-at-a-time"), "steeringMode")).toBe("one-at-a-time")
  })
})

describe("visibleSettings", () => {
  const schema = [
    setting("git.enabled", "interaction", "Enable Git Integration", "Show git branch"),
    setting("tui.vimMode", "interaction", "Vim Editing Mode"),
    setting("modelRoleStorage", "model", "Model Role Storage"),
    setting("theme.dark", "appearance", "Dark theme"),
    setting("bash.enabled", "shell", "Bash", "Run shell commands"),
  ]

  it("lists a tab without hidden or unknown-tab settings", () => {
    expect(visibleSettings(schema, "interaction", "").map((item) => item.key)).toEqual(["git.enabled", "tui.vimMode"])
    expect(visibleSettings(schema, "model", "")).toEqual([])
  })

  it("searches every tab by label, description and key, all words", () => {
    expect(visibleSettings(schema, "interaction", "shell").map((item) => item.key)).toEqual(["bash.enabled"])
    expect(visibleSettings(schema, "shell", "git branch").map((item) => item.key)).toEqual(["git.enabled"])
    expect(visibleSettings(schema, "shell", "VIMMODE").map((item) => item.key)).toEqual(["tui.vimMode"])
    expect(visibleSettings(schema, "shell", "dark")).toEqual([])
  })
})

it("names the machines whose models lack a provider, ignoring unknown ones", () => {
  const model = { provider: "anthropic", kind: "chat", id: "a", selector: "anthropic/a", name: "A", thinking: [], input: ["text"] }
  const machines = [
    { id: "local", name: "gigacomp", models: [model] },
    { id: "n2", name: "laptop", models: [] },
    { id: "n3", name: "offline" },
  ]
  expect(missingMachines("anthropic", machines).map((machine) => machine.name)).toEqual(["laptop"])
})

describe("quickRoles", () => {
  it("offers the roles whose model the chat can switch to", () => {
    const settings = { version: "", schema: [], values: { modelRoles: { default: "anthropic/opus:high" } }, network: { "modelRoles.smol": "anthropic/haiku:low", "modelRoles.slow": "openai/gpt:xhigh" } }
    expect(quickRoles(settings as never, ["anthropic/opus", "anthropic/haiku"])).toEqual([
      { id: "default", name: "Default", selector: "anthropic/opus", level: "high" },
      { id: "smol", name: "Fast", selector: "anthropic/haiku", level: "low" },
    ])
  })
})
