import { queryOptions } from "@tanstack/react-query"

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
  }
}

export const LOCAL = "local"

let onUnauthorized = () => {}

export function setUnauthorizedHandler(handler: () => void) {
  onUnauthorized = handler
}

// Mutations must pass `machine` explicitly; there is no default target.
export async function api<T>(
  path: `/api/${string}`,
  opts: { machine?: string; method?: string; body?: unknown } = {},
): Promise<T> {
  const url = new URL(path, location.origin)
  if (opts.machine && opts.machine !== LOCAL) url.searchParams.set("node", opts.machine)
  const hasBody = opts.body !== undefined
  const res = await fetch(url, {
    method: opts.method ?? (hasBody ? "POST" : "GET"),
    headers: hasBody ? { "Content-Type": "application/json" } : undefined,
    body: hasBody ? JSON.stringify(opts.body) : undefined,
  })
  const text = await res.text()
  let data: unknown
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = undefined
  }
  if (res.status === 401 && path !== "/api/login") onUnauthorized()
  if (!res.ok) {
    const error =
      data && typeof data === "object" && "error" in data && typeof data.error === "string"
        ? data.error
        : text.slice(0, 200) || res.statusText
    throw new ApiError(error, res.status)
  }
  return data as T
}

export type Status = { setup_required: boolean; authenticated: boolean }

export const statusQuery = queryOptions({
  queryKey: ["status"],
  queryFn: () => api<Status>("/api/status"),
  staleTime: 0,
})
