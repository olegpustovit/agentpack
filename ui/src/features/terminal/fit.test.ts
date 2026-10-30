import type { Terminal } from "@xterm/xterm"
import { describe, expect, it } from "vitest"
import { fitFont, nudgeFont, zoomPercent } from "./fit"

// The screen's size is a function of the font size, as xterm's cell metrics are.
function fit(stageW: number, stageH: number, screenSize: (size: number) => [number, number], start = 13, floor?: number) {
  const term = { options: { fontSize: start as number | undefined } }
  const screen = () => {
    const [offsetWidth, offsetHeight] = screenSize(term.options.fontSize ?? 13)
    return { offsetWidth, offsetHeight } as unknown as HTMLElement
  }
  const stage = { clientWidth: stageW, clientHeight: stageH } as unknown as HTMLElement
  fitFont(term as unknown as Terminal, stage, screen, floor)
  return { size: term.options.fontSize ?? 13, screen: screen() }
}

// 50 px per font px wide, 20 px per font px high.
const linear = (size: number): [number, number] => [50 * size, 20 * size]

describe("fitFont", () => {
  it("grows to fill the stage in quarter-pixel steps", () => {
    expect(fit(780, 400, linear).size).toBe(15.5)
  })

  it("caps fitted size at 16 px even when there is room for more", () => {
    expect(fit(1600, 800, linear).size).toBe(16)
  })

  it("stops at the floor and leaves overflow to scroll", () => {
    const result = fit(250, 400, linear)
    expect(result.size).toBe(6)
    expect(result.screen.offsetWidth).toBeGreaterThan(250)
  })

  it("uses the phone floor of 9 px when one is passed", () => {
    expect(fit(300, 200, linear, 13, 9).size).toBe(9)
  })

  it("always leaves the screen inside the stage unless it is at the floor", () => {
    for (const [w, h] of [[300, 200], [500, 300], [800, 400], [1100, 560]] as const) {
      for (const floor of [6, 9]) {
        const { size, screen } = fit(w, h, linear, 13, floor)
        expect(size).toBeLessThanOrEqual(16)
        expect(screen.offsetWidth <= w && screen.offsetHeight <= h || size === floor).toBe(true)
      }
    }
  })

  it("leaves the size alone when the stage or the screen has no size yet", () => {
    expect(fit(0, 400, linear).size).toBe(13)
    expect(fit(800, 400, () => [0, 0]).size).toBe(13)
  })
})

describe("zoom helpers", () => {
  it("moves in 1 px steps clamped to 6–32", () => {
    expect(nudgeFont(13, 1)).toBe(14)
    expect(nudgeFont(12.25, 1)).toBe(13.25)
    expect(nudgeFont(6, -1)).toBe(6)
    expect(nudgeFont(32, 1)).toBe(32)
  })

  it("shows the size relative to the 13 px base", () => {
    expect(zoomPercent(13)).toBe(100)
    expect(zoomPercent(16)).toBe(123)
    expect(zoomPercent(6)).toBe(46)
  })
})
