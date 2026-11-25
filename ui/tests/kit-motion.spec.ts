import { expect, test, type Locator } from "@playwright/test"
import { mockAPI } from "./helpers"

async function thumb(track: Locator) {
  return track.evaluate((element) => {
    const style = getComputedStyle(element, "::before")
    const current = element.querySelector<HTMLElement>('[aria-current="page"]')!
    return {
      duration: style.transitionDuration,
      translate: style.translate,
      width: style.width,
      target: { translate: `${current.offsetLeft}px`, width: `${current.offsetWidth}px` },
    }
  })
}

test.beforeEach(async ({ page }) => {
  await mockAPI(page)
})

test("toolbar thumb slides between views and the next pane fades in", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" })
  await page.goto("/sessions")
  const track = page.getByRole("navigation", { name: "Views", exact: true })
  await expect(track).toHaveAttribute("data-thumb-ready", "")
  const first = await thumb(track)
  expect(first).toMatchObject({ ...first.target, duration: "0.2s" })

  const pane = page.locator("main")
  await pane.evaluate((main) => {
    const animate = main.animate.bind(main)
    main.animate = (frames, options) => {
      const animation = animate(frames, options)
      main.animate = animate
      const effect = animation.effect as KeyframeEffect
      const { duration, easing } = effect.getTiming()
      const [firstFrame] = effect.getKeyframes()
      main.dataset.entryMotion = JSON.stringify({ duration, easing, opacity: firstFrame?.opacity, translate: firstFrame?.translate })
      return animation
    }
  })
  await track.getByRole("link", { name: "Library" }).click()
  await expect(pane).toHaveAttribute("data-entry-motion", JSON.stringify({
    duration: 200, easing: "cubic-bezier(0.2, 0.8, 0.2, 1)", opacity: "0", translate: "0px 4px",
  }))
  await expect(track.getByRole("link", { name: "Library" })).toHaveAttribute("aria-current", "page")
  await track.evaluate((element) => element.getAnimations({ subtree: true }).forEach((animation) => animation.finish()))
  const next = await thumb(track)
  expect(next.translate).not.toBe(first.translate)
  expect(next).toMatchObject(next.target)
})

test("reduced motion moves the thumb immediately and skips pane entry", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" })
  await page.goto("/sessions")
  const track = page.getByRole("navigation", { name: "Views", exact: true })
  await expect(track).toHaveAttribute("data-thumb-ready", "")
  const first = await thumb(track)
  await track.getByRole("link", { name: "Library" }).click()
  await expect(track.getByRole("link", { name: "Library" })).toHaveAttribute("aria-current", "page")
  const next = await thumb(track)
  expect(next.translate).not.toBe(first.translate)
  expect(next).toMatchObject({ ...next.target, duration: "0s" })
  expect(await page.locator("main").evaluate((main) => main.getAnimations().length)).toBe(0)
})

test("popovers and selects use the shared popup duration and easing", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" })
  await page.goto("/sessions")
  await page.getByRole("button", { name: "View options", exact: true }).click()
  async function expectPopupMotion(popup: Locator) {
    await expect(popup).toHaveCSS("animation-duration", "0.12s")
    await expect(popup).toHaveCSS("animation-timing-function", "cubic-bezier(0.2, 0.8, 0.2, 1)")
    await expect(popup).toHaveCSS("--tw-enter-scale", ".98")
    await expect(popup).toHaveCSS("--tw-enter-translate-y", /4px/)
  }
  await expectPopupMotion(page.getByRole("dialog", { name: "View options" }))
  await page.goto("/library")
  await page.getByRole("combobox", { name: "Sort" }).click()
  await expectPopupMotion(page.locator('[data-slot="select-content"]'))
})
