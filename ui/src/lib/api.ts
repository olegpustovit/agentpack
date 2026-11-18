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

function unreachableMessage(status: number) {
  return status === 524 ? "The machine took too long to answer. Try again in a moment." : "The gateway didn't answer. It may be restarting; try again in a moment."
}

// Go and network errors ("dial tcp …", "context canceled") mean nothing to a person; say what happened instead.
const technical = /\b(dial tcp|context (canceled|deadline exceeded)|EOF|connection (refused|reset)|broken pipe|no such host|i\/o timeout|tls:|x509|panic|runtime error|nil pointer|json: |unexpected end of JSON|goroutine)\b/i
export function readableError(text: string, status: number): string {
  if (!technical.test(text)) return text
  return status >= 502 && status <= 504 ? "The machine didn't answer. It may be offline or restarting." : "Something went wrong on the machine. Try again."
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
  if (res.status === 401 && path !== "/api/login" && path !== "/api/claim") onUnauthorized()
  if (!res.ok) {
    const error =
      data && typeof data === "object" && "error" in data && typeof data.error === "string"
        ? data.error
        : // A proxy in front of the gateway can answer with its own error page.
          /^\s*<(!doctype|html)/i.test(text)
          ? unreachableMessage(res.status)
          : text.slice(0, 200) || res.statusText
    throw new ApiError(readableError(error.trim(), res.status), res.status)
  }
  return data as T
}
