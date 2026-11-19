import { act } from "react"
import { createRoot } from "react-dom/client"
import { afterEach, expect, it, vi } from "vitest"
import { useNow } from "./use-now"

afterEach(() => vi.useRealTimers())

function Clock({ out }: { out: number[] }) {
  out.push(useNow(true))
  return null
}

it("drives every ticking component from one timer that stops with the last one", () => {
  vi.useFakeTimers({ now: 10_000 })
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  const root = createRoot(document.createElement("div"))
  const first: number[] = []
  const second: number[] = []
  act(() => root.render(<><Clock out={first} /><Clock out={second} /></>))
  expect(vi.getTimerCount()).toBe(1)
  act(() => vi.advanceTimersByTime(2000))
  expect(first.at(-1)).toBe(12_000)
  expect(second.at(-1)).toBe(12_000)
  act(() => root.unmount())
  expect(vi.getTimerCount()).toBe(0)
})
