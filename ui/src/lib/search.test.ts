import { expect, it } from "vitest"
import { looseSearch, search } from "./search"

it("keeps strings, coerces flags, drops anything else, and passes unknown keys only when loose", () => {
  const input = { q: "find", add: 1, type: 5, other: "kept" }
  expect(search({ q: "text", add: "flag", type: "text" })(input)).toEqual({ q: "find", add: "1" })
  expect(looseSearch({ q: "text", type: "text" })(input)).toEqual({ q: "find", add: 1, other: "kept" })
  expect(search({ q: "text" })({})).toEqual({})
})
