import type { SessionInfo } from "./api-types"
import { hashKey, hydrate, type DehydratedState, type QueryClient, type QueryKey } from "@tanstack/react-query"

const DB = "agentpack-cache"
const STORE = "entries"
const MAX_AGE = 24 * 60 * 60 * 1000
const SAVE_DELAY = 10_000
const CHAT_BYTES = 256 << 10
const CHATS_KEPT = 3
const shellQueries = new Set(["config", "status", "identity", "nodes", "drafts", "agent-snapshot"])
const machineLists = new Set(["sessions", "projects"])

export const cacheBuildID = document.querySelector<HTMLScriptElement>('script[type="module"][src]')?.src.match(/index-(.+)\.js$/)?.[1]

type Saved = {
  build: string
  at: number
  chat: number
  query: DehydratedState["queries"][number]
}

const streaming = new Map<string, number>()
let generation = 0

export function trackStreamingQuery(key: QueryKey): () => void {
  const hash = hashKey(key)
  streaming.set(hash, (streaming.get(hash) ?? 0) + 1)
  return () => {
    const count = streaming.get(hash) ?? 0
    if (count <= 1) streaming.delete(hash)
    else streaming.set(hash, count - 1)
  }
}

function withStore<T>(mode: IDBTransactionMode, act: (store: IDBObjectStore) => IDBRequest<T> | undefined): Promise<T | undefined> {
  return new Promise((resolve, reject) => {
    const opening = indexedDB.open(DB, 2)
    opening.onupgradeneeded = () => {
      const db = opening.result
      if (db.objectStoreNames.contains("queries")) db.deleteObjectStore("queries")
      db.createObjectStore(STORE).createIndex("recent", ["chat", "at"])
    }
    opening.onerror = () => reject(opening.error)
    opening.onsuccess = () => {
      const db = opening.result
      const tx = db.transaction(STORE, mode)
      let request: IDBRequest<T> | undefined
      try {
        request = act(tx.objectStore(STORE))
      } catch (error) {
        tx.abort()
        db.close()
        reject(error)
        return
      }
      tx.oncomplete = () => {
        db.close()
        resolve(request?.result)
      }
      tx.onerror = tx.onabort = () => {
        db.close()
        reject(tx.error)
      }
    }
  })
}

function restore(client: QueryClient, build: string, saved: Saved[], started: number) {
  if (started !== generation) return
  hydrate(client, {
    mutations: [],
    queries: saved.filter((entry) => entry.build === build && Date.now() - entry.at < MAX_AGE).map((entry) => ({
      ...entry.query,
      state: { ...entry.query.state, isInvalidated: entry.query.queryKey[0] !== "status" },
    })),
  })
}

export async function restoreQueries(client: QueryClient, build: string): Promise<void> {
  const started = generation
  try {
    const saved = await Promise.race([
      withStore<Saved[]>("readonly", (store) => store.index("recent").getAll(IDBKeyRange.bound([0, 0], [0, Infinity]))),
      new Promise<undefined>((resolve) => setTimeout(resolve, 150)),
    ])
    if (saved) restore(client, build, saved, started)
  } catch {
    // IndexedDB may be blocked.
  }
  void evictStale(build)
}

// Entries of other builds and expired ones are never restored; drop them so the store stays small.
function evictStale(build: string): Promise<unknown> {
  return withStore("readwrite", (store) => {
    const cursor = store.openCursor()
    cursor.onsuccess = () => {
      const entry = cursor.result
      if (!entry) return
      const saved = entry.value as Saved
      if (saved.build !== build || Date.now() - saved.at >= MAX_AGE) entry.delete()
      entry.continue()
    }
    return undefined
  }).catch(() => undefined)
}

export async function restoreChatQuery(client: QueryClient, build: string, key: QueryKey): Promise<void> {
  const started = generation
  try {
    const saved = await withStore<Saved>("readonly", (store) => store.get(hashKey(key)))
    if (saved) restore(client, build, [saved], started)
  } catch {
    // IndexedDB may be blocked.
  }
}

function chatTail(data: unknown): unknown {
  const page = data as { entries: unknown[]; truncated: boolean }
  const encoder = new TextEncoder()
  let size = 0
  let first = page.entries.length
  while (first > 0) {
    size += encoder.encode(JSON.stringify(page.entries[first - 1])).length + 1
    if (size > CHAT_BYTES) break
    first--
  }
  if (first === page.entries.length) return undefined
  return { entries: page.entries.slice(first), truncated: first > 0 || page.truncated }
}

export function persistQueries(client: QueryClient, build: string): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined
  let idle: number | undefined
  let observedGeneration = generation
  const dirty = new Set<string>()
  const cancelSave = () => {
    clearTimeout(timer)
    timer = undefined
    if (idle !== undefined) window.cancelIdleCallback(idle)
    idle = undefined
  }
  const scheduleSave = () => {
    if (timer !== undefined || idle !== undefined) return
    timer = setTimeout(() => {
      timer = undefined
      if (window.requestIdleCallback) idle = window.requestIdleCallback(save, { timeout: 1_000 })
      else save()
    }, SAVE_DELAY)
  }
  const save = () => {
    cancelSave()
    if (observedGeneration !== generation) {
      dirty.clear()
      observedGeneration = generation
    }
    const cache = client.getQueryCache()
    const batch: Saved[] = []
    for (const hash of dirty) {
      if (streaming.has(hash)) continue
      dirty.delete(hash)
      const query = cache.get(hash)
      if (query?.state.status !== "success") continue
      const chat = Number(query.queryKey[0] === "conversation")
      const data = chat ? chatTail(query.state.data) : query.queryKey[2] === "sessions"
        ? (query.state.data as SessionInfo[]).filter((row) => !row.provisionalStart)
        : query.state.data
      if (data === undefined) continue
      batch.push({
        build,
        at: query.state.dataUpdatedAt,
        chat,
        query: {
          queryHash: hash,
          queryKey: query.queryKey,
          dehydratedAt: Date.now(),
          state: { ...query.state, data, fetchStatus: "idle", error: null, fetchFailureReason: null },
        },
      })
    }
    if (dirty.size) scheduleSave()
    if (!batch.length) return
    const started = generation
    void withStore("readwrite", (store) => {
      if (started !== generation) return
      for (const entry of batch) store.put(entry, entry.query.queryHash)
      const cursor = store.index("recent").openKeyCursor(IDBKeyRange.bound([1, 0], [1, Infinity]), "prev")
      let kept = 0
      cursor.onsuccess = () => {
        if (!cursor.result) return
        if (++kept > CHATS_KEPT) store.delete(cursor.result.primaryKey)
        cursor.result.continue()
      }
    }).catch(() => undefined)
  }
  const unsubscribe = client.getQueryCache().subscribe((event) => {
    if (event.type !== "updated" || event.action.type !== "success") return
    if (observedGeneration !== generation) {
      dirty.clear()
      observedGeneration = generation
    }
    const [scope, , list] = event.query.queryKey
    const machineList = scope === "m" && event.query.queryKey.length === 3 && machineLists.has(String(list))
    if (scope !== "conversation" && !shellQueries.has(String(scope)) && !machineList) return
    dirty.add(event.query.queryHash)
    scheduleSave()
  })
  const onHidden = () => {
    if (document.visibilityState === "hidden") queueMicrotask(save)
  }
  document.addEventListener("visibilitychange", onHidden)
  window.addEventListener("pagehide", save)
  return () => {
    unsubscribe()
    cancelSave()
    document.removeEventListener("visibilitychange", onHidden)
    window.removeEventListener("pagehide", save)
  }
}

const resets = new Set<() => void>()
export const pullRequestStoragePrefix = "agentpack:pull-request:"

// Per-account state kept outside the query cache registers here, so the next account starts clean.
export function onSignOut(reset: () => void): void {
  resets.add(reset)
}

function resetSessionState() {
  for (const reset of resets) reset()
  try {
    for (const key of Object.keys(sessionStorage)) if (key.startsWith("agentpack:draft:")) sessionStorage.removeItem(key)
    sessionStorage.removeItem("agentpack:machine-updates")
  } catch {
    // Storage can be blocked; nothing was saved there then.
  }
  try {
    for (const key of Object.keys(localStorage)) if (key.startsWith(pullRequestStoragePrefix)) localStorage.removeItem(key)
  } catch {
    // Storage can be blocked; nothing was saved there then.
  }
}

export function forgetQueries(): void {
  generation++
  resetSessionState()
  void withStore("readwrite", (store) => store.clear()).catch(() => undefined)
}
