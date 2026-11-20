import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import { array, number, object, optional, string } from "@/lib/validate"

// Network-wide Oh My Pi settings: OMP's own schema, this machine's values and the replicated overrides.
export type OmpSetting = {
  key: string
  tab: string
  group: string
  label: string
  description: string
  type: string
  values?: unknown[]
  default?: unknown
}

export type OmpSettings = {
  version: string
  schema: OmpSetting[]
  values: Record<string, unknown>
  network: Record<string, unknown>
}

export type OmpModel = {
  provider: string
  kind: string
  id: string
  selector: string
  name: string
  contextWindow?: number
  maxTokens?: number
  thinking: string[]
  input: string[]
  cost?: { input: number; output: number }
}

const strings = (value: unknown, path: string) => array(value, path).map((item, index) => string(item, `${path}[${index}]`))

export function readOmpSettings(value: unknown): OmpSettings {
  const body = object(value, "omp settings")
  return {
    version: optional(body.version, "version", string) ?? "",
    schema: array(body.schema, "schema").map((item, index) => {
      const row = object(item, `schema[${index}]`)
      const at = (key: string) => `schema[${index}].${key}`
      return {
        key: string(row.key, at("key")),
        tab: string(row.tab, at("tab")),
        group: optional(row.group, at("group"), string) ?? "",
        label: optional(row.label, at("label"), string) ?? string(row.key, at("key")),
        description: optional(row.description, at("description"), string) ?? "",
        type: string(row.type, at("type")),
        values: optional(row.values, at("values"), array),
        default: row.default,
      }
    }),
    values: optional(body.values, "values", object) ?? {},
    network: optional(body.network, "network", object) ?? {},
  }
}

export function readOmpModels(value: unknown): OmpModel[] {
  return array(object(value, "omp models").models, "models").map((item, index) => {
    const row = object(item, `models[${index}]`)
    const at = (key: string) => `models[${index}].${key}`
    const cost = optional(row.cost, at("cost"), object)
    return {
      provider: string(row.provider, at("provider")),
      kind: optional(row.kind, at("kind"), string) ?? "chat",
      id: string(row.id, at("id")),
      selector: string(row.selector, at("selector")),
      name: optional(row.name, at("name"), string) ?? string(row.id, at("id")),
      contextWindow: optional(row.contextWindow, at("contextWindow"), number),
      maxTokens: optional(row.maxTokens, at("maxTokens"), number),
      thinking: optional(row.thinking, at("thinking"), strings) ?? [],
      input: optional(row.input, at("input"), strings) ?? ["text"],
      cost: cost && { input: number(cost.input, at("cost.input")), output: number(cost.output, at("cost.output")) },
    }
  })
}

// Values are each machine's own; the network rows are the same everywhere.
export const ompSettingsQuery = (machine: string) =>
  queryOptions({
    queryKey: ["omp-settings", machine],
    queryFn: async () => readOmpSettings(await api<unknown>("/api/omp/settings", { machine })),
  })

export const ompModelsQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "omp-models"],
    queryFn: async () => readOmpModels(await api<unknown>("/api/omp/models", { machine })),
    staleTime: 5 * 60 * 1000,
  })

// A dotted key may arrive flat ("modelRoles.default") or nested ({ modelRoles: { default } }).
function lookup(map: Record<string, unknown>, key: string): unknown {
  if (key in map) return map[key]
  let current: unknown = map
  for (const part of key.split(".")) {
    if (current === null || typeof current !== "object" || Array.isArray(current)) return undefined
    current = (current as Record<string, unknown>)[part]
  }
  return current
}

// The network override wins; otherwise this machine's own value.
export function effectiveValue(settings: OmpSettings, key: string): unknown {
  return lookup(settings.network, key) ?? lookup(settings.values, key)
}

export function isChanged(settings: OmpSettings, key: string): boolean {
  return lookup(settings.network, key) !== undefined
}

// The CLI's role table: chat roles first, then the model kinds.
export const chatRoles = [
  { id: "default", name: "Default" },
  { id: "smol", name: "Fast" },
  { id: "slow", name: "Thinking" },
  { id: "vision", name: "Vision" },
  { id: "plan", name: "Architect" },
  { id: "commit", name: "Commit" },
  { id: "tiny", name: "Tiny" },
  { id: "memory", name: "Memory" },
  { id: "task", name: "Subtask" },
  { id: "advisor", name: "Advisor" },
  { id: "widget", name: "Job widgets" },
]

// Model kinds, with the catalog kind their browser lists.
export const kindRoles = [
  { id: "image", name: "Image generation", kind: "image" },
  { id: "web", name: "Web search", kind: "search" },
  { id: "speech", name: "Speech", kind: "tts" },
  { id: "dictation", name: "Dictation", kind: "stt" },
  { id: "judge", name: "Judge", kind: "chat" },
]

export const roleKey = (role: string) => `modelRoles.${role}`

// Which catalog kinds a role's browser lists.
export function roleKinds(role: string): string[] {
  const kind = kindRoles.find((item) => item.id === role)?.kind
  if (kind) return [kind]
  return role === "tiny" || role === "memory" ? ["chat", "tiny"] : ["chat"]
}

const levels = ["off", "minimal", "low", "medium", "high", "xhigh", "max", "auto"]

export type RoleModel = { selector: string; provider: string; level: string }

// "provider/id[:level]". A model id may itself contain ":", so only a known level is a suffix.
export function parseRoleModel(value: unknown): RoleModel | undefined {
  if (typeof value !== "string" || !value.includes("/")) return undefined
  const colon = value.lastIndexOf(":")
  const suffix = colon > value.indexOf("/") ? value.slice(colon + 1) : ""
  const level = levels.includes(suffix) ? suffix : ""
  const selector = level ? value.slice(0, colon) : value
  return { selector, provider: selector.slice(0, selector.indexOf("/")), level }
}

export function formatRoleModel(selector: string, level: string): string {
  return level ? `${selector}:${level}` : selector
}

// Tabs in the CLI's order; Models is agentpack's role view, Appearance only applies to a terminal.
export const ompTabs = [
  { id: "roles", label: "Models" },
  { id: "model", label: "Model" },
  { id: "interaction", label: "Interaction" },
  { id: "context", label: "Context" },
  { id: "memory", label: "Memory" },
  { id: "files", label: "Files" },
  { id: "shell", label: "Shell" },
  { id: "tools", label: "Tools" },
  { id: "tasks", label: "Tasks" },
  { id: "providers", label: "Providers" },
]

// Role storage is per project in the CLI; the network-wide config always stores globally.
const hidden = new Set(["modelRoleStorage"])

// Settings shown on a tab, or every match for a search across label, description and key.
export function visibleSettings(schema: OmpSetting[], tab: string, query: string): OmpSetting[] {
  const shown = schema.filter((item) => !hidden.has(item.key) && ompTabs.some((known) => known.id === item.tab))
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (words.length === 0) return shown.filter((item) => item.tab === tab)
  return shown.filter((item) => {
    const text = `${item.label} ${item.description} ${item.key}`.toLowerCase()
    return words.every((word) => text.includes(word))
  })
}

// Groups in schema order. A search names the tab too, since its results span tabs.
export function groupSettings(items: OmpSetting[], searching: boolean): [string, OmpSetting[]][] {
  const groups = new Map<string, OmpSetting[]>()
  for (const item of items) {
    const tab = ompTabs.find((known) => known.id === item.tab)?.label ?? item.tab
    const name = searching ? [tab, item.group].filter(Boolean).join(" · ") : item.group || tab
    groups.set(name, [...(groups.get(name) ?? []), item])
  }
  return [...groups]
}

// A string setting whose value names a role ("@smol") picks a role instead of free text.
export function isRoleReference(value: unknown): boolean {
  return typeof value === "string" && /^@[a-z]+$/.test(value)
}

// A model's provider is available on a machine when that machine lists it (only signed-in providers are).
export function missingMachines(provider: string, machines: { id: string; name: string; models?: OmpModel[] }[]): { id: string; name: string }[] {
  return machines.filter((machine) => machine.models && !machine.models.some((model) => model.provider === provider))
}

export function formatTokens(value: number | undefined): string {
  if (!value) return "—"
  return value >= 1e6 ? `${+(value / 1e6).toFixed(1)}M` : `${Math.round(value / 1e3)}K`
}

export function formatPrice(cost: OmpModel["cost"]): string {
  if (!cost) return "—"
  return cost.input === 0 && cost.output === 0 ? "free" : `$${cost.input}·${cost.output}`
}

// "None", the items themselves when they fit, or a count.
export function summarize(value: unknown): string {
  if (Array.isArray(value)) {
    if (value.length === 0) return "None"
    return value.length <= 3 && value.every((item) => typeof item === "string") ? value.join(", ") : `${value.length} items`
  }
  if (value && typeof value === "object") {
    const count = Object.keys(value).length
    return count === 0 ? "None" : `${count} ${count === 1 ? "entry" : "entries"}`
  }
  return "None"
}

// The cached settings with one override written (or removed when value is undefined), for optimistic edits.
export function withOverride(settings: OmpSettings, key: string, value: unknown): OmpSettings {
  const network = { ...settings.network }
  delete network[key]
  const dot = key.indexOf(".")
  const parent = dot > 0 ? network[key.slice(0, dot)] : undefined
  if (parent && typeof parent === "object" && !Array.isArray(parent)) {
    const nested: Record<string, unknown> = { ...parent }
    delete nested[key.slice(dot + 1)]
    network[key.slice(0, dot)] = nested
  }
  if (value !== undefined) network[key] = value
  return { ...settings, network }
}

export type QuickRole = { id: string; name: string; selector: string; level: string }

// Roles a chat can switch to: those with a model the chat's agent offers.
export function quickRoles(settings: OmpSettings, models: string[]): QuickRole[] {
  return chatRoles.flatMap((role) => {
    const model = parseRoleModel(effectiveValue(settings, roleKey(role.id)))
    return model && models.includes(model.selector) ? [{ ...role, selector: model.selector, level: model.level }] : []
  })
}
