import { describe, expect, it } from "vitest"
import { formatBytes } from "./machine-detail"

describe("formatBytes", () => {
  it("rounds large sizes and keeps one decimal below 10 GB", () => {
    expect(formatBytes(31.4 * 1024 ** 3)).toBe("31 GB")
    expect(formatBytes(2.25 * 1024 ** 3)).toBe("2.3 GB")
  })
})
