import { describe, expect, it } from "vitest"
import { imageFilesFromList, promptRequest, retryPromptInput } from "./composer-attach"

describe("composer attach", () => {
  it("keeps every allowed image while dropping unsupported types", () => {
    const files = [
      new File(["x"], "a.png", { type: "image/png" }),
      new File(["x"], "unsafe.svg", { type: "image/svg+xml" }),
      new File(["x"], "b.webp", { type: "image/webp" }),
    ]
    expect(imageFilesFromList(files).map((file) => file.name)).toEqual(["a.png", "b.webp"])
  })
  it("keeps names and dimensions in the multi-image ACP request", () => {
    expect(promptRequest("Review these", [
      { sha: "one", name: "one.png", mime: "image/png", width: 10, height: 20 },
      { sha: "two", name: "two.webp", mime: "image/webp", width: 30, height: 40 },
    ], false)).toEqual({
      path: "prompt/images",
      body: {
        text: "Review these",
        images: [
          { sha: "one", name: "one.png", width: 10, height: 20 },
          { sha: "two", name: "two.webp", width: 30, height: 40 },
        ],
      },
    })
  })

  it("retries a recorded image only when the ACP agent accepts image blocks", () => {
    const prompt = { image: { blob: `sha256:${"a".repeat(64)}`, mime: "image/webp", width: 40, height: 30 } }
    expect(retryPromptInput(prompt, false)).toBeUndefined()
    expect(retryPromptInput(prompt, true)).toEqual({
      text: "",
      attachments: [{ sha: "a".repeat(64), name: undefined, mime: "image/webp", width: 40, height: 30 }],
    })
  })

  it("retries recorded multi-image prompts with every named blob", () => {
    expect(retryPromptInput({
      text: { text: "Review these" },
      images: [
        { blob: "sha256:one", name: "one.png", width: 10, height: 20, kind: "image" },
        { blob: "sha256:two", name: "two.webp", width: 30, height: 40, kind: "resource_link" },
      ],
    }, false)).toEqual({
      text: "Review these",
      attachments: [
        { sha: "one", name: "one.png", mime: "image/png", width: 10, height: 20 },
        { sha: "two", name: "two.webp", mime: "image/png", width: 30, height: 40 },
      ],
    })
  })

  it("retries a recorded file link with its stored name", () => {
    expect(retryPromptInput({ resource_link: { blob: "sha256:beef", name: "screen.png" } }, false)).toEqual({
      text: "",
      attachments: [],
      resourceLink: { sha: "beef", name: "screen.png" },
    })
  })
})
