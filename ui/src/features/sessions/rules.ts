import { ApiError } from "@/lib/api"
import type { ListedSession } from "./group"

// Row action availability. Ported from app.js: agy and non-interactive sessions have no browser
// terminal (app.js:1193), and the menu rules follow app.js:1463-1470.
export function sessionRules(session: ListedSession) {
  const offline = session.offline
  const agy = session.harness === "agy"
  const nonInteractive = session.state.interactive === false
  return {
    rename: !offline,
    restart: !offline && !nonInteractive,
    stop: !offline && session.state.running,
    reconnect: !offline && !agy && !nonInteractive,
    install: !offline && !agy,
    save: !offline,
  }
}

// app.js:300-318. A rejected request (4xx) drops the restart's operation id. A server error or a
// transport failure keeps it, so the retry is the same intent and the gateway can de-duplicate it.
export function reusesOperationId(error: unknown): boolean {
  return !(error instanceof ApiError && error.status >= 400 && error.status < 500)
}
