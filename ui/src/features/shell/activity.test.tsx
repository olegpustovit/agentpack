import { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

let container: HTMLDivElement
let root: Root
let hud: typeof import("@/lib/hud").hud

beforeEach(async () => {
  vi.resetModules()
  const { ActivityViewer } = await import("./activity")
  hud = (await import("@/lib/hud")).hud
  vi.useFakeTimers()
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true)
  container = document.createElement("div")
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<ActivityViewer />))
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it("shows nothing until an outcome, then settles into the activity history button", () => {
  expect(container.querySelector("button")).toBeNull()
  act(() => hud.success("Updated the machine"))
  expect(container.querySelector("button")?.getAttribute("aria-label")).toBe("Updated the machine. Show activity")
  act(() => vi.advanceTimersByTime(4_100))
  expect(container.querySelector("button")?.getAttribute("aria-label")).toBe("Activity")
})

it("keeps failures visible and removes a failed outcome when retried", () => {
  const retry = vi.fn()
  act(() => hud.error("Couldn't update the machine", retry))
  act(() => vi.advanceTimersByTime(10_000))
  expect(container.querySelector("button")?.getAttribute("aria-label")).toBe("Couldn't update the machine. Show activity")
  const button = [...container.querySelectorAll("button")].find((element) => element.textContent === "Retry")
  expect(button).toBeDefined()
  act(() => button?.click())
  expect(retry).toHaveBeenCalledOnce()
  expect(container.querySelector('[aria-label="Couldn\'t update the machine. Show activity"]')).toBeNull()
})

it("keeps an unresolved failure ahead of later successful outcomes", () => {
  act(() => {
    hud.error("Couldn't update the machine")
    for (let i = 0; i < 21; i++) hud.success(`Saved project ${i}`)
  })
  expect(container.querySelector("button")?.getAttribute("aria-label")).toBe("Couldn't update the machine. Show activity")
  const dismiss = container.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')
  act(() => dismiss?.click())
  expect(container.querySelector("button")?.getAttribute("aria-label")).toBe("Saved project 20. Show activity")
})
