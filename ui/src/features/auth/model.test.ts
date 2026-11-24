import { describe, expect, it } from "vitest"
import { loginErrors } from "./model"

const fields = { email: "owner@example.com", password: "a-long-password", confirm: "a-long-password", currentPassword: "" }

describe("account forms", () => {
  it("requires the email and uses byte limits for a claim password", () => {
    expect(loginErrors({ claimed: false, legacy: false }, fields)).toEqual({})
    expect(loginErrors({ claimed: false, legacy: false }, { ...fields, email: " " })).toHaveProperty("email")
    expect(loginErrors({ claimed: false, legacy: false }, { ...fields, password: "界".repeat(4), confirm: "界".repeat(4) })).toEqual({})
    expect(loginErrors({ claimed: false, legacy: false }, { ...fields, password: "界".repeat(25) })).toHaveProperty("password")
    expect(loginErrors({ claimed: false, legacy: false }, { ...fields, confirm: "different" })).toHaveProperty("confirm")
  })

  it("requires proof of the legacy password and explains that claiming preserves it", () => {
    expect(loginErrors({ claimed: false, legacy: true }, fields)).toHaveProperty("currentPassword")
    expect(loginErrors({ claimed: false, legacy: true }, { ...fields, currentPassword: "short" })).toHaveProperty("currentPassword")
    expect(loginErrors({ claimed: false, legacy: true }, { ...fields, password: "", confirm: "", currentPassword: "legacy-password" })).toEqual({})
  })

  it("does not require confirmation for an ordinary login", () => {
    expect(loginErrors({ claimed: true, legacy: false }, { ...fields, confirm: "" })).toEqual({})
    expect(loginErrors({ claimed: true, legacy: false }, { ...fields, password: "" })).toHaveProperty("password")
  })
})
