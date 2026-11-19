// Checks for gateway and peer responses at the trust boundary, small enough for the first chunk.
// A wrong required field fails the query; an optional field sent as null reads as absent.
export type Fields = Record<string, unknown>

function invalid(path: string): never {
  throw new Error(`unexpected ${path} in gateway response`)
}

export function object(value: unknown, path: string): Fields {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Fields) : invalid(path)
}

export function array(value: unknown, path: string): unknown[] {
  return Array.isArray(value) ? value : invalid(path)
}

export function string(value: unknown, path: string): string {
  return typeof value === "string" ? value : invalid(path)
}

export function number(value: unknown, path: string): number {
  return typeof value === "number" ? value : invalid(path)
}

export function boolean(value: unknown, path: string): boolean {
  return typeof value === "boolean" ? value : invalid(path)
}

export function optional<T>(value: unknown, path: string, read: (value: unknown, path: string) => T): T | undefined {
  return value === undefined || value === null ? undefined : read(value, path)
}
