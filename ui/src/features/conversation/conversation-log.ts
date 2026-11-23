import { useQueryClient } from "@tanstack/react-query"
import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react"
import { reconnectDelay, useConversationEvents } from "@/features/events/use-events"
import { LOCAL } from "@/lib/api"
import type { WaitItem } from "@/lib/api-types"
import { array, boolean, number, object, string } from "@/lib/validate"
import { trackStreamingQuery } from "@/lib/persist"
import { joinHistory, olderConversationPage, type ConversationPage } from "./queries"
import type { LogEntry, ThreadItem } from "./model"
import { reduceThread } from "./reducer"

/** The chat's state as the owning worker's fold computed it; the browser's own reducer is the fallback for older owners. */
export type DerivedState = { conversation_state: string; working: boolean; needs_you: boolean; waiting: WaitItem[]; through: number }

export function parseDerived(data: string): DerivedState | undefined {
  try {
    const fields = object(JSON.parse(data), "derived")
    return {
      conversation_state: string(fields.conversation_state, "derived.conversation_state"),
      working: boolean(fields.working, "derived.working"),
      needs_you: boolean(fields.needs_you, "derived.needs_you"),
      waiting: fields.waiting == null ? [] : (array(fields.waiting, "derived.waiting") as WaitItem[]),
      through: number(fields.through, "derived.through"),
    }
  } catch {
    return undefined
  }
}

// Appends a chat's live entries to its cached tail: through the shared event stream, or its own
// EventSource on a gateway without one. Reports the connection so the tail query stops refetching,
// and the server's derived state while connected.
export function useConversationStream(machine: string, sessionId: string, enabled: boolean, setStreamConnected: (connected: boolean) => void, setDerived: (derived: DerivedState | undefined) => void) {
  const queryClient = useQueryClient()
  const subscribe = useConversationEvents()
  const [legacyStream, setLegacyStream] = useState(false)
  useEffect(() => {
    if (!enabled) return
    const key = ["conversation", machine, sessionId]
    const onEntry = (data: string) => {
      let entry: LogEntry
      try {
        entry = JSON.parse(data) as LogEntry
      } catch {
        return
      }
      queryClient.setQueryData<ConversationPage>(key, (current) => {
        const rows = current?.entries ?? []
        if (entry.seq <= (rows.at(-1)?.seq ?? 0)) return current
        return { entries: [...rows, entry], truncated: current?.truncated ?? false }
      })
    }
    const onTruncated = () => {
      queryClient.setQueryData<ConversationPage>(key, (current) => current && !current.truncated ? { ...current, truncated: true } : current)
    }
    // The stream skipped entries the worker could no longer send: reload the tail instead of showing a hole.
    const onGap = () => void queryClient.refetchQueries({ queryKey: key, exact: true })
    const onDerived = (data: string) => {
      const derived = parseDerived(data)
      if (derived) setDerived(derived)
    }
    const connectedTo = (connected: boolean) => {
      // A frame from a closed stream would outlive the state it described.
      if (!connected) setDerived(undefined)
      setStreamConnected(connected)
    }
    if (subscribe && !legacyStream) {
      let untrack: (() => void) | undefined
      const unsubscribe = subscribe({
        machine, sessionId, onEntry, onTruncated, onGap, onDerived,
        onConnection: (connected) => {
          if (connected) {
            if (!untrack) untrack = trackStreamingQuery(key)
          } else {
            untrack?.()
            untrack = undefined
          }
          connectedTo(connected)
        },
        onFallback: () => setLegacyStream(true),
      })
      return () => {
        unsubscribe()
        untrack?.()
        untrack = undefined
        setDerived(undefined)
      }
    }
    let source: EventSource | undefined
    let untrack: (() => void) | undefined
    let retry: ReturnType<typeof setTimeout> | undefined
    let attempt = 0
    const close = () => {
      clearTimeout(retry)
      source?.close()
      source = undefined
      untrack?.()
      untrack = undefined
      connectedTo(false)
    }
    const syncVisibility = () => {
      if (document.visibilityState === "hidden") {
        close()
        return
      }
      if (source) return
      const url = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/conversation`, location.origin)
      url.searchParams.set("stream", "1")
      url.searchParams.set("since", String(queryClient.getQueryData<ConversationPage>(key)?.entries.at(-1)?.seq ?? 0))
      if (machine && machine !== LOCAL) url.searchParams.set("node", machine)
      source = new EventSource(url)
      untrack = trackStreamingQuery(key)
      source.onopen = () => {
        attempt = 0
        connectedTo(true)
      }
      source.onerror = () => {
        connectedTo(false)
        if (source?.readyState !== EventSource.CLOSED) return
        close()
        retry = setTimeout(syncVisibility, reconnectDelay(attempt))
        attempt = attempt + 1
      }
      source.onmessage = (event) => onEntry(event.data)
      source.addEventListener("truncated", onTruncated)
      source.addEventListener("gap", onGap)
      source.addEventListener("derived", (event: MessageEvent<string>) => onDerived(event.data))
    }
    document.addEventListener("visibilitychange", syncVisibility)
    syncVisibility()
    return () => {
      document.removeEventListener("visibilitychange", syncVisibility)
      close()
    }
  }, [enabled, legacyStream, machine, queryClient, sessionId, setDerived, setStreamConnected, subscribe])
}

// Earlier pages, loaded as the thread scrolls up, joined to the cached tail; the reader stays on the same
// line as they are prepended, and a thread opened mid-turn pages back to the turn's prompt.
export function useHistoryPaging(
  machine: string,
  sessionId: string,
  tail: ConversationPage | undefined,
  threadScrollRef: RefObject<HTMLDivElement | null>,
  stickToBottomRef: RefObject<boolean>,
) {
  const historyKey = `${machine}\0${sessionId}`
  const [history, setHistory] = useState<{ key: string; entries: LogEntry[]; more: boolean; loading: boolean; error?: unknown }>()
  const older = history?.key === historyKey ? history : undefined
  const tailEntries = tail?.entries ?? []
  const entries = joinHistory(older?.entries ?? [], tailEntries)
  const hasOlder = (older ? older.more : Boolean(tail?.truncated)) && (entries[0]?.seq ?? 1) > 1
  // Scroll height and offset before older entries were prepended, so the reader stays on the same line.
  const scrollAnchorRef = useRef<{ height: number; top: number } | null>(null)
  const transcript = reduceThread(historyKey, entries)
  useLayoutEffect(() => {
    const element = threadScrollRef.current
    if (!element || tailEntries.length === 0 || element.querySelector("[data-thread-virtual]")) return
    if (stickToBottomRef.current) element.scrollTop = element.scrollHeight
  }, [tailEntries])
  useLayoutEffect(() => {
    const element = threadScrollRef.current
    const anchor = scrollAnchorRef.current
    if (!element || !anchor) return
    scrollAnchorRef.current = null
    if (element.querySelector("[data-thread-virtual]")) return
    element.scrollTop = anchor.top + element.scrollHeight - anchor.height
  }, [older?.entries])

  // Scrolling never retries a failed page; the Retry button does.
  async function loadOlder(retry = false, complete?: (loaded: LogEntry[]) => boolean) {
    const first = entries[0]
    if (!first || !hasOlder || older?.loading || (older?.error && !retry)) return
    const previous = older?.entries ?? []
    let kept = previous
    let before = first.seq
    setHistory({ key: historyKey, entries: kept, more: true, loading: true })
    let more: boolean
    let loaded: LogEntry[]
    do {
      let page: ConversationPage
      try {
        page = await olderConversationPage(machine, sessionId, before)
      } catch (error) {
        setHistory({ key: historyKey, entries: previous, more: true, loading: false, error })
        return
      }
      kept = [...page.entries, ...kept]
      loaded = joinHistory(kept, tailEntries)
      const next = page.entries[0]?.seq
      more = page.truncated && next !== undefined && next > 1 && next < before
      before = next ?? before
    } while (more && complete && !complete(loaded))
    const element = threadScrollRef.current
    if (element) scrollAnchorRef.current = { height: element.scrollHeight, top: element.scrollTop }
    setHistory({ key: historyKey, entries: kept, more, loading: false })
    return loaded
  }
  // A tool-heavy turn can outgrow the first page, which then starts mid-turn: no prompt, and
  // subagents without the titles recorded when they started. Page back to the turn's prompt.
  const hasUserMessage = (items: ThreadItem[]) => items.some((item) => item.kind === "user_message_chunk")
  const turnStartMissing = hasOlder && !hasUserMessage(transcript.items)
  useEffect(() => {
    if (turnStartMissing) void loadOlder(false, (rows) => hasUserMessage(reduceThread(historyKey, rows).items))
  }, [turnStartMissing])
  return { historyKey, older, entries, hasOlder, transcript, loadOlder }
}
