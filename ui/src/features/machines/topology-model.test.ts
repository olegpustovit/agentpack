import { describe, expect, it } from "vitest"
import type { TopologyPeer } from "@/lib/api-types"
import { edgeOf, layoutDiagram, machinePeers, reasonOf, ROW_H, sessionCount, stateWord } from "./topology-model"

function peer(name: string, state: string, connection = "", relay = ""): TopologyPeer {
  return { id: name, name, os: "linux", state, connection, relay, sessions: 0 }
}

describe("stateWord", () => {
  it("names both peer states in words", () => {
    expect(["reachable", "offline"].map(stateWord)).toEqual(["Reachable", "Offline"])
  })
})

describe("edgeOf", () => {
  it("labels relay with its region and dashes it, and labels direct solid", () => {
    expect(edgeOf(peer("a", "reachable", "relay", "fra"))).toEqual({ label: "relay fra", dashed: true })
    expect(edgeOf(peer("b", "reachable", "direct"))).toEqual({ label: "direct", dashed: false })
  })

  it("does not call a path with unknown connection direct", () => {
    expect(edgeOf(peer("c", "reachable"))).toEqual({ label: "path unknown", dashed: false })
  })
})

describe("machinePeers", () => {
  it("lists exactly the sidebar machines with their online state, enriched from the tailnet", () => {
    const peers = [{ ...peer("mac", "reachable", "direct"), os: "macOS" }, peer("tv", "offline"), peer("phone", "no-gateway")]
    const result = machinePeers(peers, [{ id: "mac", name: "mac", online: false }, { id: "gone", name: "gone", online: false }])
    expect(result.map((p) => [p.name, p.os, p.state])).toEqual([["mac", "macOS", "offline"], ["gone", "", "offline"]])
  })
})

describe("reasonOf", () => {
  it("gives the last contact for offline peers only", () => {
    expect(reasonOf(peer("mac", "reachable"))).toBe("")
    expect(reasonOf({ ...peer("build", "offline"), last_seen: new Date(Date.now() - 3 * 86_400_000).toISOString() })).toBe("Last seen 3 days ago")
  })
})

describe("sessionCount", () => {
  it("uses the topology count when it is set, the sessions query otherwise", () => {
    expect(sessionCount(2, 5)).toBe(2)
    expect(sessionCount(0, 5)).toBe(5)
    expect(sessionCount(0, 0)).toBe(0)
    expect(sessionCount(0, undefined)).toBeUndefined()
  })
})

describe("layoutDiagram", () => {
  it("places rows 44 px apart with one edge per reachable peer", () => {
    const layout = layoutDiagram([peer("mac", "reachable", "direct"), peer("build", "offline"), peer("fed", "reachable", "relay", "fra")])
    expect(layout.nodes.map((node) => node.y)).toEqual([2, 2 + ROW_H, 2 + 2 * ROW_H])
    expect(layout.edges.map((edge) => edge.peer.name)).toEqual(["mac", "fed"])
    expect(layout.edges[1]).toMatchObject({ label: "relay fra", dashed: true })
  })
})
