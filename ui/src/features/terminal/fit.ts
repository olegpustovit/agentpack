import type { Terminal } from "@xterm/xterm"

export const BASE_FONT = 13
export const MIN_FONT = 6
export const MAX_FONT = 32
// Fit never zooms past this; only ± and 1:1 reach larger sizes.
const MAX_FIT = 16

// Scales the font so the screen fills the stage, then steps down until it fits or reaches the floor.
// The screen is never scaled with CSS transforms, so the grid stays crisp and selectable.
export function fitFont(term: Terminal, stage: HTMLElement, screen: () => HTMLElement | null, floor = MIN_FONT): void {
  const availW = stage.clientWidth
  const availH = stage.clientHeight
  if (!availW || !availH) return
  let size = term.options.fontSize ?? BASE_FONT
  for (let i = 0; i < 4; i++) {
    const el = screen()
    // An unrendered screen measures 0; dividing by it would zoom to MAX_FIT.
    if (!el || !el.offsetWidth || !el.offsetHeight) return
    const ratio = Math.min(availW / el.offsetWidth, availH / el.offsetHeight)
    if (ratio >= 1 && ratio < 1.02) break
    const next = Math.min(MAX_FIT, Math.max(floor, Math.floor(size * ratio * 4) / 4))
    if (next === size) break
    size = next
    term.options.fontSize = size
  }
  while (size > floor) {
    const el = screen()
    if (!el || (el.offsetWidth <= availW && el.offsetHeight <= availH)) break
    size -= 0.25
    term.options.fontSize = size
  }
}

// ± and the phone floor both go through here: one pixel per step, never outside 6–32.
export function nudgeFont(size: number, delta: number): number {
  return Math.min(MAX_FONT, Math.max(MIN_FONT, size + delta))
}

export function zoomPercent(size: number): number {
  return Math.round((size / BASE_FONT) * 100)
}
