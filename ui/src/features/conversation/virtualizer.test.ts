import { expect, it, vi } from "vitest"
import { elementScroll, Virtualizer } from "@tanstack/virtual-core"

// thread-view drives these private lifecycle hooks; a dependency bump that renames or reshapes them must fail here, not in scrolling.
it("keeps the private lifecycle hooks thread-view's adapter calls", () => {
  const element = document.createElement("div")
  const unobserve = vi.fn()
  const observeElementRect = vi.fn(() => unobserve)
  const instance = new Virtualizer<HTMLDivElement, HTMLDivElement>({
    count: 1, getScrollElement: () => element, estimateSize: () => 96, scrollToFn: elementScroll,
    observeElementRect, observeElementOffset: () => () => {},
  })
  expect(instance._didMount).toHaveLength(0)
  expect(instance._willUpdate).toHaveLength(0)
  const dispose = instance._didMount()
  instance._willUpdate()
  expect(observeElementRect).toHaveBeenCalledWith(instance, expect.any(Function))
  dispose()
  expect(unobserve).toHaveBeenCalledOnce()
})
