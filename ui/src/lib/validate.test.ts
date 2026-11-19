import { expect, it } from "vitest"
import { array, number, object, optional, string } from "./validate"

it("rejects a wrong required field and reads null optionals as absent", () => {
  const peer = object({ id: "p", rtt_ms: null }, "peer")
  expect(string(peer.id, "id")).toBe("p")
  expect(optional(peer.rtt_ms, "rtt_ms", number)).toBeUndefined()
  expect(() => string(5, "peers[0].name")).toThrow("unexpected peers[0].name")
  expect(() => array({}, "peers")).toThrow()
})
