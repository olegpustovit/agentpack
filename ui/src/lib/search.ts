// Search params are a string or absent, like zod's .optional().catch(undefined), without zod in the first chunk.
// A "flag" key also accepts a number or boolean: the router's parser JSON-decodes "1" to 1.
type Kind = "text" | "flag"

function read(kind: Kind, value: unknown): string | undefined {
  if (typeof value === "string") return value
  return kind === "flag" && (typeof value === "number" || typeof value === "boolean") ? String(value) : undefined
}

// Keeps only the listed keys.
export function search<S extends Record<string, Kind>>(spec: S) {
  return (input: Record<string, unknown>): { [K in keyof S]?: string } => {
    const out: { [K in keyof S]?: string } = {}
    for (const key in spec) {
      const value = read(spec[key]!, input[key])
      if (value !== undefined) out[key] = value
    }
    return out
  }
}

// Also passes unknown keys through, for layouts whose children add their own.
export function looseSearch<S extends Record<string, Kind>>(spec: S) {
  const known = search(spec)
  return (input: Record<string, unknown>): { [K in keyof S]?: string } & Record<string, unknown> => {
    const rest = { ...input }
    for (const key in spec) delete rest[key]
    return { ...rest, ...known(input) }
  }
}
