import { describe, expect, it } from "vitest"
import { allowListLabel, replicationStatus, sharingLabel } from "./model"

const online = { id: "n2", name: "oracle-vm", online: true }
const offline = { id: "n3", name: "lab-pi", online: false }

describe("settings labels", () => {
  it("describes sharing state", () => {
    expect(sharingLabel(true)).toBe("Enabled for allowed Tailscale identities")
    expect(sharingLabel(false)).toBe("Disabled. Local CLI sessions still work.")
  })

  it("lists the allow list or says there is none", () => {
    expect(allowListLabel(["tag:agents", "n2"])).toBe("tag:agents, n2")
    expect(allowListLabel([])).toBe("No additional machines or tags")
    expect(allowListLabel(null)).toBe("No additional machines or tags")
  })
})

describe("replicationStatus", () => {
  it("reports receipts and reachability for enrolled peers", () => {
    expect(replicationStatus(online, { node: "n2", receipt_count: 3, digests: [] })).toBe("3 verified object receipts · online")
    expect(replicationStatus(offline, { node: "n3", receipt_count: 1, digests: [] })).toBe("1 verified object receipt · offline")
  })

  it("says what an unenrolled peer needs", () => {
    expect(replicationStatus(online, undefined)).toBe("Not enrolled")
    expect(replicationStatus(offline, undefined)).toBe("Offline · enrollment unavailable")
  })
})
