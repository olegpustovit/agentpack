import { describe, expect, it } from "vitest"
import { TerminalPool, type PoolEntry } from "./pool"

type Fake = PoolEntry & { id: string; disposed: number }

function fake(id: string): Fake {
  const entry: Fake = { id, disposed: 0, dispose: () => entry.disposed++, focus: () => {},
    reconnect: () => {} }
  return entry
}

describe("TerminalPool", () => {
  it("dispose ends one session, so a later open starts fresh", () => {
    const pool = new TerminalPool<Fake>()
    const first = pool.get("local", "s1", () => fake("s1"))
    pool.dispose("local", "s1")
    expect(first.disposed).toBe(1)
    expect(pool.get("local", "s1", () => fake("again")).id).toBe("again")
  })

  it("returns the live entry on a hit without creating another", () => {
    const pool = new TerminalPool<Fake>()
    const first = pool.get("local", "s1", () => fake("s1"))
    let created = 0
    expect(pool.get("local", "s1", () => (created++, fake("s1")))).toBe(first)
    expect(created).toBe(0)
  })

  it("keeps four and evicts the least recently used on the fifth", () => {
    const pool = new TerminalPool<Fake>()
    const made = new Map<string, Fake>()
    const open = (id: string) =>
      pool.get("local", id, () => {
        const entry = fake(id)
        made.set(id, entry)
        return entry
      })
    for (const id of ["s1", "s2", "s3", "s4"]) open(id)
    open("s1") // s1 becomes most recent, so s2 is now the oldest
    open("s5")
    expect(made.get("s2")?.disposed).toBe(1)
    for (const id of ["s1", "s3", "s4", "s5"]) expect(made.get(id)?.disposed).toBe(0)
  })

  it("disposes every entry on disposeAll and forgets them", () => {
    const pool = new TerminalPool<Fake>()
    const made: Fake[] = []
    for (const id of ["a", "b"]) {
      pool.get("local", id, () => {
        const entry = fake(id)
        made.push(entry)
        return entry
      })
    }
    pool.disposeAll()
    expect(made.map((entry) => entry.disposed)).toEqual([1, 1])
    let created = 0
    pool.get("local", "a", () => (created++, fake("a")))
    expect(created).toBe(1)
  })
})
