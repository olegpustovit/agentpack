import { describe, expect, test } from "vitest"
import { parseDerived } from "./conversation-log"

describe("parseDerived", () => {
  test("reads the owner's fold and drops a frame of the wrong shape", () => {
    expect(parseDerived(`{"conversation_state":"idle","working":false,"needs_you":false,"waiting":[{"kind":"wake","until":5}],"through":7}`))
      .toEqual({ conversation_state: "idle", working: false, needs_you: false, waiting: [{ kind: "wake", until: 5 }], through: 7 })
    expect(parseDerived(`{"conversation_state":"working","working":true,"needs_you":false,"through":2}`)?.waiting).toEqual([])
    expect(parseDerived(`{"working":"yes"}`)).toBeUndefined()
    expect(parseDerived("not json")).toBeUndefined()
  })
})
