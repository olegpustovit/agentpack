import { dehydrate, hydrate, type DehydratedState, type Query, type QueryClient } from "@tanstack/react-query"

// The query cache survives reloads in IndexedDB, so a reload paints the last-known lists and chats at once
// and refetches behind them. Only successful reads are kept, for one UI build, for a day.
const DB = "agentpack-cache"
const STORE = "queries"
const KEY = "client"
const MAX_AGE = 24 * 60 * 60 * 1000
const SAVE_DELAY = 1000
// Conversations are the largest entries; the most recently loaded ones are what a reload reopens.
const CHATS_KEPT = 8
// IndexedDB is usually a few milliseconds; a slower one must not hold the first paint.
const RESTORE_WAIT = 150

type Saved = { build: string; at: number; state: DehydratedState }

function withStore<T>(mode: IDBTransactionMode, act: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    const opening = indexedDB.open(DB, 1)
    opening.onupgradeneeded = () => opening.result.createObjectStore(STORE)
    opening.onerror = () => reject(opening.error)
    opening.onsuccess = () => {
      const db = opening.result
      const tx = db.transaction(STORE, mode)
      const request = act(tx.objectStore(STORE))
      tx.oncomplete = () => {
        db.close()
        resolve(request.result)
      }
      tx.onerror = tx.onabort = () => {
        db.close()
        reject(tx.error)
      }
    }
  })
}

export async function restoreQueries(client: QueryClient, build: string): Promise<void> {
  try {
    const saved = await Promise.race([
      withStore<Saved | undefined>("readonly", (store) => store.get(KEY)),
      new Promise<undefined>((resolve) => setTimeout(resolve, RESTORE_WAIT)),
    ])
    if (saved && saved.build === build && Date.now() - saved.at < MAX_AGE) {
      hydrate(client, saved.state)
      // Shown at once, but anything may have changed while the page was closed: mounting refetches it behind.
      // The sign-in status stays fresh so the shell's guard never waits; a 401 anywhere still signs out.
      void client.invalidateQueries({ refetchType: "none", predicate: (query) => query.queryKey[0] !== "status" })
    }
  } catch {
    // Storage can be blocked or unavailable; the app then loads from the network as before.
  }
}

function keep(chats: Set<Query>) {
  return (query: Query) => query.state.status === "success" && (query.queryKey[0] !== "conversation" || chats.has(query))
}

// Saves at most once a second while queries change. Returns the unsubscribe.
export function persistQueries(client: QueryClient, build: string): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined
  const save = () => {
    timer = undefined
    const chats = new Set(
      client.getQueryCache().findAll({ queryKey: ["conversation"] })
        .sort((a, b) => b.state.dataUpdatedAt - a.state.dataUpdatedAt)
        .slice(0, CHATS_KEPT),
    )
    const saved: Saved = { build, at: Date.now(), state: dehydrate(client, { shouldDehydrateQuery: keep(chats) }) }
    void withStore("readwrite", (store) => store.put(saved, KEY)).catch(() => undefined)
  }
  return client.getQueryCache().subscribe(() => {
    timer ??= setTimeout(save, SAVE_DELAY)
  })
}

// Called on sign-out and on any 401, so the next person on this browser never sees the last one's data.
export function forgetQueries(): void {
  try {
    void withStore("readwrite", (store) => store.delete(KEY)).catch(() => undefined)
  } catch {
    // Nothing was stored.
  }
}
