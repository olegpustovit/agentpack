export type ConfigOption =
  | {
      type: "select"
      id: string
      name: string
      category?: string
      value: string
      items: { value: string; label: string; disabledReason?: string }[]
    }
  | {
      type: "boolean"
      id: string
      name: string
      category?: string
      value: boolean
    }

export type ConfigRole = "permissions" | "model" | "effort" | "other"

export function configRole(option: ConfigOption): ConfigRole {
  const category = (option.category ?? option.id).toLowerCase().replace(/[-_ ]/g, "")
  if (category === "mode") return "permissions"
  if (category === "model") return "model"
  if (category === "thoughtlevel" || category === "effort") return "effort"
  return "other"
}

// Modes that let the agent act without asking: Claude's bypassPermissions, Codex full-access, yolo modes.
export function dangerousMode(item: { value: string; label: string }): boolean {
  return /bypass|full.?access|yolo|dangerous/i.test(`${item.value} ${item.label}`)
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" ? value as Record<string, unknown> : undefined
}

function parseOptions(raw: unknown): ConfigOption[] {
  if (!Array.isArray(raw)) return []
  const out: ConfigOption[] = []
  for (const entry of raw) {
    const row = record(entry)
    if (!row) continue
    const id = String(row.id ?? "")
    if (!id) continue
    const name = String(row.name ?? id)
    const category = typeof row.category === "string" && row.category ? row.category : undefined
    if (row.type === "boolean") {
      if (typeof row.currentValue === "boolean") out.push({ type: "boolean", id, name, ...(category ? { category } : {}), value: row.currentValue })
      continue
    }
    if (row.type !== "select") continue
    const items: Extract<ConfigOption, { type: "select" }>["items"] = []
    if (Array.isArray(row.options)) {
      for (const choice of row.options) {
        const option = record(choice)
        if (!option) continue
        const value = String(option.value ?? option.id ?? "")
        const disabledReason = typeof option.disabledReason === "string" && option.disabledReason
          ? option.disabledReason
          : option.disabled === true || option.available === false
            ? typeof option.description === "string" ? option.description : "Unavailable for this agent"
            : undefined
        if (value) items.push({ value, label: String(option.name ?? option.label ?? value), ...(disabledReason && { disabledReason }) })
      }
    }
    if (items.length > 0) {
      const current = String(row.currentValue ?? "")
      out.push({ type: "select", id, name, ...(category ? { category } : {}), value: current || items[0]!.value, items })
    }
  }
  return out
}

export function parseConfigOptions(raw: unknown): ConfigOption[] {
  const response = record(raw)
  if (Array.isArray(raw)) return parseOptions(raw)
  if (!response) return []
  if (response.configOptions !== undefined) return parseOptions(response.configOptions)

  const modes = record(response.modes)
  const currentModeId = typeof modes?.currentModeId === "string" ? modes.currentModeId : ""
  if (!currentModeId || !Array.isArray(modes?.availableModes)) return []
  const items = modes.availableModes.flatMap((entry) => {
    const mode = record(entry)
    return typeof mode?.id === "string" && mode.id
      ? [{ value: mode.id, label: typeof mode.name === "string" ? mode.name : mode.id }]
      : []
  })
  return items.some((item) => item.value === currentModeId)
    ? [{ type: "select", id: "mode", name: "Permissions", value: currentModeId, items }]
    : []
}

