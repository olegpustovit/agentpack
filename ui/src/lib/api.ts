import { recordAPI, traceRequest } from "./telemetry"

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
    this.name = "ApiError"
  }
}

export const LOCAL = "local"

let onUnauthorized = () => {}

export function setUnauthorizedHandler(handler: () => void) {
  onUnauthorized = handler
}

function unreachableMessage(status: number, method: string, path: string, machine?: string) {
  const target = machine && machine !== LOCAL ? ` on node ${machine}` : ""
  const request = `${method} ${path}${target}`
  return status === 524
    ? `The gateway timed out during ${request} (HTTP 524). Check the gateway and machine, then retry.`
    : `The gateway's reverse proxy returned an HTML error during ${request} (HTTP ${status}). The response had no Agentpack error detail; check the gateway or reverse-proxy logs, then retry.`
}

// Mutations must pass `machine` explicitly; there is no default target.
export async function api<T>(
  path: `/api/${string}`,
  opts: { machine?: string; method?: string; body?: unknown; query?: Record<string, string> } = {},
): Promise<T> {
  const url = new URL(path, location.origin)
  if (opts.machine && opts.machine !== LOCAL) url.searchParams.set("node", opts.machine)
  if (opts.query) {
    for (const [key, value] of Object.entries(opts.query)) {
      url.searchParams.set(key, value)
    }
  }
  const hasBody = opts.body !== undefined
  const method = opts.method ?? (hasBody ? "POST" : "GET")
  const traceparent = traceRequest()
  let res: Response
  try {
    res = await fetch(url, {
      method,
      headers: hasBody ? { "Content-Type": "application/json", traceparent } : { traceparent },
      body: hasBody ? JSON.stringify(opts.body) : undefined,
    })
  } catch (error) {
    recordAPI(method, path, traceparent, 0, error)
    throw error
  }
  recordAPI(method, path, traceparent, res.status)
  const text = await res.text().catch((error) => {
    if (res.ok) recordAPI(method, path, traceparent, 0, error)
    throw error
  })
  let data: unknown
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = undefined
  }
  if (res.status === 401 && path !== "/api/login" && path !== "/api/setup") onUnauthorized()
  if (!res.ok) {
    const error =
      data && typeof data === "object" && "error" in data && typeof data.error === "string"
        ? data.error
        : // A proxy in front of the gateway can answer with its own error page.
          /^\s*<(!doctype|html)/i.test(text)
          ? unreachableMessage(res.status, method, path, opts.machine)
          : text.slice(0, 200) || res.statusText
    throw new ApiError(error, res.status)
  }
  return data as T
}
