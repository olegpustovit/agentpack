import type { TerminalSession } from "./terminal-session"

// Live terminals kept per tab. A fifth one evicts the least recently used.
const CAPACITY = 4

export type PoolEntry = { dispose(): void }

// Generic, and imports only a type, so the root bundle can import the pool without xterm.
// `create` runs only on a miss; the session itself is built by the lazily loaded pane.
export class TerminalPool<T extends PoolEntry = TerminalSession> {
  // Map insertion order is recency: the first key is the least recently used.
  readonly #entries = new Map<string, T>()

  get(machine: string, sessionId: string, create: () => T): T {
    const key = `${machine}\0${sessionId}`
    const existing = this.#entries.get(key)
    if (existing) {
      this.#entries.delete(key)
      this.#entries.set(key, existing)
      return existing
    }
    const entry = create()
    this.#entries.set(key, entry)
    for (const [oldKey, oldest] of this.#entries) {
      if (this.#entries.size <= CAPACITY) break
      this.#entries.delete(oldKey)
      oldest.dispose()
    }
    return entry
  }

  disposeAll() {
    const entries = [...this.#entries.values()]
    this.#entries.clear()
    for (const entry of entries) entry.dispose()
  }
}

// One pool per tab. Sign-out disposes every session through it.
export const terminalPool = new TerminalPool<TerminalSession>()
