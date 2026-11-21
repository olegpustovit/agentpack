// @vitest-environment jsdom
import { expect, it } from "vitest"
import { terminalShortcut } from "./shortcuts"

it("recognizes terminal shortcuts with the same modifiers inside xterm and the composer", () => {
  const xterm = document.createElement("div")
  xterm.className = "xterm"
  const input = xterm.appendChild(document.createElement("textarea"))
  const composer = document.createElement("textarea")
  for (const target of [input, composer]) {
    for (const mac of [true, false]) {
      const mod = mac ? { metaKey: true } : { ctrlKey: true }
      const other = mac ? { ctrlKey: true } : { metaKey: true }
      for (const [keys, expected] of [
        [{ key: "j", ...mod }, "toggle"],
        [{ key: "J", ...mod, shiftKey: true }, "next"],
        [{ key: "j", ...other }, undefined],
        [{ key: "j", ...mod, altKey: true }, undefined],
        [{ key: "j", metaKey: true, ctrlKey: true }, undefined],
        [{ code: "Backquote", ctrlKey: true }, "toggle"],
        [{ code: "Backquote", ctrlKey: true, shiftKey: true }, "new"],
        [{ code: "Backquote", ctrlKey: true, altKey: true }, undefined],
        [{ code: "Backquote" }, undefined],
      ] as const) {
        const received: ReturnType<typeof terminalShortcut>[] = []
        target.addEventListener("keydown", (event) => received.push(terminalShortcut(event, mac)), { once: true })
        target.dispatchEvent(new KeyboardEvent("keydown", keys))
        expect(received).toEqual([expected])
      }
    }
  }
})
