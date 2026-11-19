import { afterEach, describe, expect, it } from "vitest"
import { loadAttachments, saveAttachments } from "./upload-blob"

const sha = "a".repeat(64)

describe("draft attachments", () => {
  afterEach(() => sessionStorage.clear())

  it("round-trips through storage and previews from the server", () => {
    saveAttachments("k", [{ sha, mime: "image/png", width: 4, height: 3, name: "shot.png", previewUrl: "blob:x" }])
    expect(sessionStorage.getItem("k")).not.toContain("blob:")
    expect(loadAttachments("k", (value) => `/blobs/${value}`)).toEqual([
      { sha, mime: "image/png", width: 4, height: 3, name: "shot.png", previewUrl: `/blobs/${sha}` },
    ])
  })

  it("clears the key when empty and ignores junk", () => {
    saveAttachments("k", [])
    expect(sessionStorage.getItem("k")).toBeNull()
    sessionStorage.setItem("k", JSON.stringify([{ sha: "../../etc" }, 3]))
    expect(loadAttachments("k", (value) => value)).toEqual([])
  })
})
