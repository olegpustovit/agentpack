import { recordAPI, traceRequest } from "./telemetry"

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly headers?: Headers,
  ) {
    super(message)
    this.name = "ApiError"
  }
}

// A 404 from a router without the route (an older gateway or worker), not a missing record.
// The gateway's /api/ fallback says "not found"; Go's default mux says "404 page not found".
export function routeMissing(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404 && (error.message === "not found" || error.message === "404 page not found")
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

// The error a failed response carries: the gateway's JSON error, or its plain text.
export function responseError(res: Response, text: string): ApiError {
  let error = ""
  try {
    const data: unknown = JSON.parse(text)
    if (data && typeof data === "object" && "error" in data && typeof data.error === "string") error = data.error
  } catch {
    // Not JSON.
  }
  // A proxy in front of the gateway can answer with its own error page.
  if (!error) error = /^\s*<(!doctype|html)/i.test(text) ? unreachableMessage(res.status) : text.slice(0, 200) || res.statusText
  return new ApiError(readableError(error.trim(), res.status), res.status, res.headers)
}

type ApiOptions = {
  machine?: string
  method?: string
  body?: unknown
  // Sent as the raw request body with its own type, instead of JSON.
  upload?: Blob
  query?: Record<string, string>
  idempotencyKey?: string
  signal?: AbortSignal
}

// The one fetch behind every gateway call: node routing, trace, telemetry, the sign-in redirect and error text.
// A non-2xx response throws; callers read the body of a successful one.
async function send(path: `/api/${string}`, opts: ApiOptions) {
  const url = new URL(path, location.origin)
  if (opts.machine && opts.machine !== LOCAL) url.searchParams.set("node", opts.machine)
  for (const [key, value] of Object.entries(opts.query ?? {})) url.searchParams.set(key, value)
  const hasBody = opts.body !== undefined
  const method = opts.method ?? (hasBody || opts.upload ? "POST" : "GET")
  const traceparent = traceRequest()
  const headers = new Headers({ traceparent })
  if (hasBody) headers.set("Content-Type", "application/json")
  if (opts.upload) headers.set("Content-Type", opts.upload.type || "application/octet-stream")
  if (opts.idempotencyKey) headers.set("Idempotency-Key", opts.idempotencyKey)
  let res: Response
  try {
    res = await fetch(url, { method, headers, body: opts.upload ?? (hasBody ? JSON.stringify(opts.body) : undefined), signal: opts.signal })
  } catch (error) {
    recordAPI(method, path, traceparent, 0, error)
    throw error
  }
  recordAPI(method, path, traceparent, res.status)
  if (res.status === 401 && path !== "/api/login" && path !== "/api/claim") onUnauthorized()
  if (!res.ok) throw responseError(res, await res.text().catch(() => ""))
  const text = () =>
    res.text().catch((error: unknown) => {
      recordAPI(method, path, traceparent, 0, error)
      throw error
    })
  return { res, text }
}

// A successful response and its body text.
export async function request(path: `/api/${string}`, opts: ApiOptions = {}): Promise<{ res: Response; text: string }> {
  const { res, text } = await send(path, opts)
  return { res, text: await text() }
}

// Mutations must pass `machine` explicitly; there is no default target.
export async function api<T>(path: `/api/${string}`, opts: ApiOptions = {}): Promise<T> {
  const { text } = await request(path, opts)
  try {
    return (text ? JSON.parse(text) : undefined) as T
  } catch {
    return undefined as T
  }
}

// POSTs body and hands each NDJSON line to onLine as it arrives, so a slow check reports progress.
export async function apiLines(path: `/api/${string}`, body: unknown, onLine: (line: unknown) => void): Promise<void> {
  const { res } = await send(path, { body })
  if (!res.body) throw new ApiError(res.statusText, res.status)
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
  let buffer = ""
  for (;;) {
    const { done, value } = await reader.read()
    buffer += value ?? ""
    const lines = buffer.split("\n")
    buffer = done ? "" : (lines.pop() ?? "")
    for (const line of lines) if (line.trim()) onLine(JSON.parse(line))
    if (done) return
  }
}

// What a failed request says to a person. A peer without the route (an older version) reads as Needs update.
export function errorText(error: unknown, fallback = "the request did not complete"): string {
  if (routeMissing(error)) return "Needs update"
  return error instanceof Error ? error.message : fallback
}
